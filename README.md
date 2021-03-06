Zoom
====

[![GoDoc](https://godoc.org/github.com/albrow/zoom?status.svg)](https://godoc.org/github.com/albrow/zoom)

Version: 0.7.4

A blazing-fast, lightweight ORM for Go built on Redis.

Requires redis version >= 2.8.9 and Go version >= 1.0.

Full documentation is available on
[godoc.org](http://godoc.org/github.com/albrow/zoom).

**WARNING:** this isn't done yet and may change significantly before the official release. There is no
promise of backwards compatibility until version 1.0. I do not advise using Zoom for production or
mission-critical applications. Feedback and pull requests are welcome :)

Table of Contents
-----------------

- [Philosophy](#philosophy)
- [Installation](#installation)
- [Getting Started](#getting-started)
- [Working with Models](#working-with-models)
- [Enforcing Thread-Safety](#enforcing-thread-safety)
- [Running Queries](#running-queries)
- [Relationships](#relationships)
- [Testing & Benchmarking](#testing--benchmarking)
- [Example Usage](#example-usage)
- [TODO](#todo)
- [License](#license)


Philosophy
----------

If you want to build a high-performing, low latency application, but still want some of the ease
of an ORM, Zoom is for you!

Zoom allows you to:

- Persistently save structs of any type
- Retrieve structs from the database
- Preserve relationships between structs
- Preform *limited* queries

Zoom consciously makes the trade off of using more memory in order to increase performance.
Zoom stores all data in memory at all times, so if your machine runs out of memory, zoom will
either crash or start using swap space (resulting in huge performance penalties). 
Zoom does not do sharding (but might in the future), so be aware that memory could be a
hard constraint for larger applications.

Zoom is a high-level library and abstracts away more complicated aspects of the Redis API. For example,
it manages its own connection pool, performs transactions when possible, and automatically converts
structs to and from a format suitable for the database. If needed, you can still execute redis commands
directly.

If you want to use advanced or complicated SQL queries, Zoom is not for you. For example, Zoom
currently lacks an equivalent of the SQL keywords `IN` and `OR`. Although support for more
types of queries may be added in the future, it is not a high priority.


Installation
------------

First, you must install Redis on your system. [See installation instructions](http://redis.io/download).
By default, Zoom will use a tcp/http connection on localhost:6379 (same as the Redis default). The latest
version of Redis is recommended.

To install Zoom itself:

    go get github.com/albrow/zoom
    
This will pull the current master branch, which is (most likely) working but is quickly changing.


Getting Started
---------------

First, add github.com/albrow/zoom to your import statement:

``` go
import (
    ...
    github.com/albrow/zoom
)
```

Then, call zoom.Init somewhere in your app initialization code, e.g. in the main function. You must
also call zoom.Close when your application exits, so it's a good idea to use defer.

``` go
func main() {
    // ...
    zoom.Init(nil)
    defer zoom.Close()
    // ...
}
```

The Init function takes a *zoom.Configuration struct as an argument. Here's a list of options and their
defaults:

``` go
type Configuration struct {
	Address       string // Address to connect to. Default: "localhost:6379"
	Network       string // Network to use. Default: "tcp"
	Database      int    // Database id to use (using SELECT). Default: 0
}
```

If possible, it is ***strongly recommended*** that you use a unix socket connection instead of tcp.
Redis is roughly [50% faster](http://redis.io/topics/benchmarks) this way. To connect with a unix
socket, you must first configure redis to accept socket connections (typically on /tmp/redis.sock).
If you are unsure how to do this, refer to the [official redis docs](http://redis.io/topics/config)
for help. You might also find the [redis quickstart guide](http://redis.io/topics/quickstart) helpful,
especially the bottom sections.

To use unix sockets with Zoom, simply pass in "unix" as the Network and "/tmp/unix.sock" as the Address:

``` go
config := &zoom.Configuration {
	Address: "/tmp/redis.sock",
	Network: "unix",
}
zoom.Init(config)
```


Working with Models
-------------------

### Creating Models

In order to save a struct using Zoom, you need to embed an anonymous DefaultData field. DefaultData
gives you an Id and getters and setters for that id, which are required in order to save the model
to the database. Here's an example of a Person model:

``` go
type Person struct {
    Name string
    zoom.DefaultData
}
```

Because of the way zoom uses reflection, all the fields you want to save need to be public.

You must also call zoom.Register so that Zoom can spec out the different model types and the relations between them.
You only need to do this once per type. For example, somewhere in your initialization sequence (e.g. in the main
function) put the following:

``` go
// register the *Person type under the default name "Person"
if err := zoom.Register(&Person{}); err != nil {
    // handle error
}
```

Now the *Person type will be associated with the string name "Person." You can also use the RegisterName
funcion to specify a custom name for the model type.

### Saving Models

To persistently save a Person model to the databse, simply call zoom.Save.

``` go
p := &Person{Name: "Alice"}
if err := zoom.Save(p); err != nil {
    // handle error
}
```

### Finding a Single Model

Zoom will automatically assign a random, unique id to each saved model. To retrieve a model by id,
use the FindById function, which also requires the name associated with the model type. The return
type is interface{} so you may need to type assert.

``` go
result, err := zoom.FindById("Person", "a_valid_person_id")
if err != nil {
    // handle error
}

// type assert
person, ok := result.(*Person)
if !ok {
    // handle !ok
}
```

Alternatively, you can use the ScanById function to avoid type assertion. It expects a pointer
to a struct as an argument(some registered model type).


``` go
p := &Person{}
if err := zoom.ScanById("a_valid_person_id", p); err != nil {
    // handle error
}
```

### Deleting Models

To delete a model you can just use the Delete function:

``` go
if err := zoom.Delete(person); err != nil {
	// handle err
}
```

Or if you know the model's id, use the DeleteById function:

``` go
if err := zoom.DeleteById("Person", "some_person_id"); err != nil {
	// handle err
}
```

Enforcing Thread-Safety
-----------------------

### How it Works

If you wish to perform thread-safe updates, you can embed zoom.Sync into your model. zoom.Sync provides a default
implementation of a Syncer. A Syncer consists of a unique identifier which is a reference to a global mutex map. By
default the identifier is modelType + ":" + id. If a model implements the Syncer interface, any time the model is
retrieved from the database, zoom will call Lock on the mutex referenced by Syncer. The effect is that you can
gauruntee that only one reference to a given model is active at any given time.

**IMPORTANT**: When you embed zoom.Sync into a model, you must remember to call Unlock() when you are done making
changes to the model.

### Example

Here's what a model with an embedded Syncer should look like:

``` go
type Person struct {
	Age int
	Name string
	zoom.DefaultData
	zoom.Sync
}
```

And here's what a thread-safe update would look like:

``` go
func UpdatePerson() {
	p := &Person{}
	// You must unlock the mutex associated with the id when you are
	// done making changes. Using defer is sometimes a good way to do this.
	defer p.Unlock()
	// ScanById implicitly calls Lock(), so it will not return until any other
	// references to the same model id are unlocked.
	if err := zoom.ScanById("some valid id", p); err != nil {
		// handle err
	}
	p.Name = "Bill"
	p.Age = 27
	if err := zoom.Save(p); err != nil {
		// handle err
	}	
}
```


Running Queries
---------------

### The Query Object

Zoom provides a useful abstraction for querying the database. You create queries by using the NewQuery
constuctor, where you must pass in the name corresponding to the type of model you want to query. For now,
Zoom only supports queries on a single type of model at a time.

You can add one or more query modifiers to the query, such as Order, Limit, and Filter. These methods return
the query itself, so you can chain them together. Any errors due to invalid arguments in the query modifiers
will be remembered and returned when you attempt to run the query.

Finally, you run the query using a query finisher method, such as Run or Scan.


### Finding all Models of a Given Type 

To retrieve a list of all persons, create a query and don't apply any modifiers.
The return type of Run is interface{}, but the underlying type is a slice of Model,
i.e. a slice of pointers to structs. You may need a type assertion.

``` go
results, err := zoom.NewQuery("Person").Run()
if err != nil {
    // handle error
}

// type assert each element. something like:
persons := make([]*Person, len(results))
for i, result := range results {
    if person, ok := result.(*Person); !ok {
        // handle !ok
    }
    persons[i] = person
}
```

You can use the Scan method if you want to avoid a type assertion. Scan expects a pointer to
a slice of pointers to some model type.

``` go
persons := make([]*Person, 0)
if _, err := zoom.NewQuery("Person").Scan(persons); err != nil {
	// handle err
}
```

### Using Query Modifiers

You can chain a query object together with one or more different modifiers. Here's a list
of all the available modifiers:

- Order
- Limit
- Offset
- Include
- Exclude
- Filter

You can run a query with one of the following query finishers:

- Run
- Scan
- IdsOnly
- Count
- RunOne
- ScanOne

Here's an example of a more complicated query using several modifiers:

``` go
q := zoom.NewQuery("Person").Order("-Name").Filter("Age >=", 25).Limit(10)
result, err := q.Run()
```

You might be able to guess what each of these methods do, but if anything is not obvious,
full documentation on the different modifiers and finishers is available on
[godoc.org](http://godoc.org/github.com/albrow/zoom).


Relationships
-------------

Relationships in Zoom are simple. There are no special return types or functions for using relationships.
What you put in is what you get out.

### One-to-One Relationships

For these examples we're going to introduce two new struct types:

``` go
// The PetOwner struct
type PetOwner struct {
	Name  string
	Pet   *Pet    // *Pet should also be a registered type
	zoom.DefaultData
}

// The Pet struct
type Pet struct {
	Name   string
	zoom.DefaultData
}

```

Assuming you've registered both the *PetOwner and *Pet types, Zoom will automatically set up a relationship
when you save a PetOwner with a valid Pet. (The Pet must have an id)

``` go
// create a new PetOwner and a Pet
owner := &PetOwner{Name: "Bob"}
pet := &Pet{Name: "Spot"}

// save the pet
if err := zoom.Save(pet); err != nil {
	// handle err
}

// set the owner's pet
owner.Pet = pet

// save the owner
if err := zoom.Save(owner); err != nil {
	// handle err
}
```

Now if you retrieve the pet owner by it's id, the pet attribute will persist as well.

For now, Zoom does not support reflexivity of one-to-one relationships. So if you want pet ownership to be
bidirectional (i.e. if you want an owner to know about its pet **and** a pet to know about its owner),
you would have to manually set up both relationships.

``` go
ownerCopy := &PetOwner{}
if err := zoom.ScanById("the_id_of_above_pet_owner", ownerCopy); err != nil {
	// handle err
}

// the Pet attribute is still set
fmt.Println(ownerCopy.Pet.Name)

// Output:
//	Spot
```

### One-to-Many Relationships

One-to-many relationships work similarly. This time we're going to use two new struct types in the examples.

``` go
// The Parent struct
type Parent struct {
	Name     string
	Children []*Child  // *Child should also be a registered type
	zoom.DefaultData
}

// The Child struct
type Child struct {
	Name   string
	zoom.DefaultData
}
```

Assuming you register both the *Parent and *Child types, Zoom will automatically set up a relationship
when you save a parent with some children (as long as each child has an id). Here's an example:

``` go
// create a parent and two children
parent := &Parent{Name: "Christine"}
child1 := &Child{Name: "Derick"}
child2 := &Child{Name: "Elise"}

// save the children
if err := zoom.Save(child1, child2); err != nil {
	// handle err
}

// assign the children to the parent
parent.Children = append(parent.Children, child1, child2)

// save the parent
if err := zoom.Save(parent); err != nil {
	// handle err
}
```

Again, Zoom does not support reflexivity. So if you wanted a child to know about its parent, you would have
to set up and manage the relationship manually. This might change in the future.

Now when you retrieve a parent by id, it's children field will automatically be populated. So getting
the children again is straight forward.

``` go
parentCopy := &Parent{}
if err := zoom.ScanById("the_id_of_above_parent", parentCopy); err != nil {
	// handle error
}

// now you can access the children normally
for _, child := range parentCopy.Children {
	fmt.Println(child.Name)
}

// Output:
//	Derick
//	Elise

```

For a Parent with a lot of children, it may take a long time to get each Child from the database. If
this is the case, it's a good idea to use a query with the Exclude modifier when you don't intend to
use the children.

``` go
parents := make([]*Parent, 0)
q := zoom.NewQuery("Parent").Filter("Id =", "the_id_of_above_parent").Exclude("Children")
if err := q.Scan(parents); err != nil {
	// handle error
}

// Since it was excluded, Children is empty.
fmt.Println(parents[0].Children)

// Output:
//	[]
```

### Many-to-Many Relationships

There is nothing special about many-to-many relationships. They are simply made up of multiple one-to-many
relationships.


Testing & Benchmarking
----------------------

### Running the Tests:

To run the tests, make sure you're in the root directory for Zoom and run:

```
go test .
```   

If everything passes, you should see something like:

    ok  	github.com/albrow/zoom	0.355s
    
If any of the tests fail, please [open an issue](https://github.com/albrow/zoom/issues/new) and
describe what happened.

By default, tests and benchmarks will run on localhost:6379 and use database #9. You can change the address,
network, and database used with flags. So to run on a unix socket at /tmp/redis.sock and use database #3,
you could use:

```
go test . -network unix -address /tmp/redis.sock -database 3
```

### Running the Benchmarks:

To run the benchmarks, again make sure you're in the root directory and run:

```
go test . -bench .
```   

You can use the same flags as above to change the network, address, and database used.

You should see some runtimes for various operations. If you see an error or if the build fails, please
[open an issue](https://github.com/albrow/zoom/issues/new).

Here are the results from my laptop (2.3GHz quad-core i7, 8GB RAM) using a socket connection with Redis set
to append-only mode:

```
BenchmarkConnection         20000000	      93.7 ns/op
BenchmarkPing                 100000	     24472 ns/op
BenchmarkSet                   50000	     32703 ns/op
BenchmarkGet                  100000	     25795 ns/op
BenchmarkSave                  50000	     59899 ns/op
BenchmarkMSave100               2000	    908596 ns/op
BenchmarkFindById              50000	     41050 ns/op
BenchmarkMFindById100	        2000	    671383 ns/op
BenchmarkDeleteById	          50000	     48800 ns/op
BenchmarkMDeleteById100	        2000	    617435 ns/op
BenchmarkFindAllQuery10	       10000	    165903 ns/op
BenchmarkFindAllQuery1000	      500	   7224478 ns/op
BenchmarkFindAllQuery100000	     2	 850699127 ns/op
BenchmarkCountAllQuery10	   100000	     29838 ns/op
BenchmarkCountAllQuery1000	   100000	     29739 ns/op
BenchmarkCountAllQuery100000	100000	     29798 ns/op
```

Currently, there are not many benchmarks for queries; I'm working on adding more.

The results of the benchmark can vary widely from system to system. You should run your
own benchmarks that are closer to your use case to get a real sense of how Zoom
will perform for you. The speeds above are already pretty fast, but improving them is
one of the top priorities for this project.

    
Example Usage
-------------

I have built an [example json/rest application](https://github.com/albrow/peeps-negroni)
which uses the latest version of zoom. It is a simple example that doesn't use all of
zoom's features, but should be good enough for understanding how zoom can work in a
real application.


TODO
----

Ordered generally by priority, here's what I'm working on:

- Improve performance and get as close as possible to raw redis
- Add more benchmarks
- Add godoc compatible examples in the test files
- Support callbacks (BeforeSave, AfterSave, BeforeDelete, AfterDelete, etc.)
- Implement high-level watching for record changes
- Add option to make relationships reflexive (inverseOf struct tag?)
- Add a dependent:delete struct tag
- Support AND and OR operators on Filters
- Support combining queries into a single transaction
- Support automatic sharding

If you have an idea or suggestion for a feature, please [open an issue](https://github.com/albrow/zoom/issues/new)
and describe it.


License
-------

Zoom is licensed under the MIT License. See the LICENSE file for more information.
