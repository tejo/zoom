// Copyright 2013 Alex Browne.  All rights reserved.
// Use of this source code is governed by the MIT
// license, which can be found in the LICENSE file.

// File query.go contains code related to the query abstraction.

package zoom

import (
	"errors"
	"fmt"
	"github.com/garyburd/redigo/redis"
	"math"
	"reflect"
	"strings"
)

// RunScanner is an interface implemented by Query
// It may also be implemented by other types in the future.
type RunScanner interface {
	Run() (interface{}, error)
	Scan(interface{}) error
}

// Query represents a query which will retrieve some models from
// the database. A Query may consist of one or more query modifiers
// and can be run in several different ways with different query
// finishers.
type Query struct {
	modelSpec modelSpec
	includes  []string
	excludes  []string
	order     order
	limit     uint
	offset    uint
	filters   []filter
	err       error
}

type order struct {
	fieldName string
	redisName string
	orderType orderType
	indexed   bool
	indexType indexType
}

type orderType int

const (
	ascending = iota
	descending
)

type filter struct {
	fieldName   string
	redisName   string
	filterType  filterType
	filterValue reflect.Value
	indexType   indexType
	byId        bool
}

type filterType int

const (
	equal = iota
	notEqual
	greater
	less
	greaterOrEqual
	lessOrEqual
)

var filterSymbols = map[string]filterType{
	"=":  equal,
	"!=": notEqual,
	">":  greater,
	"<":  less,
	">=": greaterOrEqual,
	"<=": lessOrEqual,
}

// used as a prefix for alpha index tricks
// this is a string which equals ASCII DEL
var delString string = string([]byte{byte(127)})

// NewQuery is used to construct a query. modelName should be the
// name of a registered model. The query returned can be chained
// together with one or more query modifiers, and then run using
// the Run, Scan, Count, or Ids only methods. If no query modifiers
// are used, running the query will return all models that match the
// type corresponding to modelName in uspecified order. Will return
// an error if modelName is not the name of some registered model
// type.
func NewQuery(modelName string) *Query {
	q := &Query{}
	spec, found := modelSpecs[modelName]
	if !found {
		q.setErrorIfNone(NewModelNameNotRegisteredError(modelName))
	} else {
		q.modelSpec = spec
	}
	return q
}

// Order specifies a field by which to sort the records and the order in which
// records should be sorted. fieldName should be a field in the struct type specified by
// the modelName argument in the query constructor. By default, the records are sorted
// by ascending order. To sort by descending order, put a negative sign before
// the field name. Zoom can only sort by fields which have been indexed, i.e. those which
// have the `zoom:"index"` struct tag. However, in the future this may change.
// Only one order may be specified per query. However in the future, secondary orders may be
// allowed, and will take effect when two or more models have the same value for the primary
// order field. Order will set an error on the query if the fieldName is invalid, if another
// order has already been applied to the query, or if the fieldName specified does not correspond
// to an indexed field. There is currently a bug where Order may not work correctly when
// combined with filters.
func (q *Query) Order(fieldName string) *Query {
	if q.order.fieldName != "" {
		// TODO: allow secondary sort orders?
		q.setErrorIfNone(errors.New("zoom: error in Query.Order: previous order already specified. Only one order per query is allowed."))
	}
	var ot orderType
	if strings.HasPrefix(fieldName, "-") {
		ot = descending
		// remove the "-" prefix
		fieldName = fieldName[1:]
	} else {
		ot = ascending
	}
	if _, found := q.modelSpec.field(fieldName); found {
		indexType, found := q.modelSpec.indexTypeForField(fieldName)
		if !found {
			// the field was not indexed
			// TODO: add support for ordering unindexed fields in some cases?
			msg := fmt.Sprintf("zoom: error in Query.Order: field %s in type %s is not indexed. Can only order by indexed fields", fieldName, q.modelSpec.modelType.String())
			q.setErrorIfNone(errors.New(msg))
		}
		redisName, _ := q.modelSpec.redisNameForFieldName(fieldName)
		q.order = order{
			fieldName: fieldName,
			redisName: redisName,
			orderType: ot,
			indexType: indexType,
			indexed:   true,
		}
	} else {
		// fieldName was invalid
		msg := fmt.Sprintf("zoom: error in Query.Order: could not find field %s in type %s", fieldName, q.modelSpec.modelType.String())
		q.setErrorIfNone(errors.New(msg))
	}

	return q
}

// Limit specifies an upper limit on the number of records to return.
// If amount is 0, no limit will be applied. There is currently a bug
// where Limit may not work correctly when combined with filters.
func (q *Query) Limit(amount uint) *Query {
	q.limit = amount
	return q
}

// Offset specifies a starting index from which to start counting records that
// will be returned. There is currently a bug where Offset may not work correctly
// when combined with filters.
func (q *Query) Offset(amount uint) *Query {
	q.offset = amount
	return q
}

// Include specifies one or more field names which will be read from the database
// and scanned into the resulting models when the query is run. Field names which
// are not specified in Include will not be read or scanned. You can only use one
// of Include or Exclude, not both on the same query.
func (q *Query) Include(fields ...string) *Query {
	if len(q.excludes) > 0 {
		q.setErrorIfNone(errors.New("zoom: cannot use both Include and Exclude modifiers on a query"))
		return q
	}
	q.includes = append(q.includes, fields...)
	return q
}

// Exclude specifies one or more field names which will *not* be read from the
// database and scanned. Any other fields *will* be read and scanned into the
// resulting models when the query is run. You can only use one of Include or
// Exclude, not both on the same query.
func (q *Query) Exclude(fields ...string) *Query {
	if len(q.includes) > 0 {
		q.setErrorIfNone(errors.New("zoom: cannot use both Include and Exclude modifiers on a query"))
		return q
	}
	q.excludes = append(q.excludes, fields...)
	return q
}

// Filter applies a filter to the query, which will cause the query to only
// return models with attributes matching the expression. filterString should be
// an expression which includes a fieldName, a space, and an operator in that
// order. Operators must be one of "=", "!=", ">", "<", ">=", or "<=". If multiple
// filters are applied to the same query, the query will only return models which
// have matches for ALL of the filters. I.e. applying multiple filters is logially
// equivalent to combining them with a AND or INTERSECT operator. There are currently
// some bugs, and Filter may not work correctly when combined with Count, Order,
// Limit, or Offset.
func (q *Query) Filter(filterString string, value interface{}) *Query {
	fieldName, operator, err := splitFilterString(filterString)
	if err != nil {
		q.setErrorIfNone(err)
		return q
	}
	if fieldName == "Id" {
		// special case for Id
		return q.filterById(operator, value)
	}
	f := filter{
		fieldName: fieldName,
	}
	// get the filterType based on the operator
	if typ, found := filterSymbols[operator]; !found {
		q.setErrorIfNone(errors.New("zoom: invalid operator in fieldStr. should be one of =, !=, >, <, >=, or <=."))
		return q
	} else {
		f.filterType = typ
	}
	// get the redisName based on the fieldName
	if redisName, found := q.modelSpec.redisNameForFieldName(fieldName); !found {
		msg := fmt.Sprintf("zoom: invalid fieldName in filterString.\nType %s has no field %s", q.modelSpec.modelType.String(), fieldName)
		q.setErrorIfNone(errors.New(msg))
		return q
	} else {
		f.redisName = redisName
	}
	// get the indexType based on the fieldName
	if indexType, found := q.modelSpec.indexTypeForField(fieldName); !found {
		msg := fmt.Sprintf("zoom: filters are only allowed on indexed fields.\n%s.%s is not indexed.", q.modelSpec.modelType.String(), fieldName)
		q.setErrorIfNone(errors.New(msg))
		return q
	} else {
		f.indexType = indexType
	}
	// get type of the field and make sure it matches type of value arg
	// Here we iterate through pointer inderections. This is so you can
	// just pass in a primative instead of a pointer to a primative for
	// filtering on fields which have pointer values.
	structField, _ := q.modelSpec.field(fieldName)
	fieldType := structField.Type
	valueType := reflect.TypeOf(value)
	valueVal := reflect.ValueOf(value)
	for valueType.Kind() == reflect.Ptr {
		valueType = valueType.Elem()
		valueVal = valueVal.Elem()
		if !valueVal.IsValid() {
			q.setErrorIfNone(errors.New("zoom: invalid value arg for Filter. Is it a nil pointer?"))
			return q
		}
	}
	if valueType != fieldType {
		msg := fmt.Sprintf("zoom: invalid value arg for Filter. Parsed type of value (%s) does not match type of field (%s).", valueType.String(), fieldType.String())
		q.setErrorIfNone(errors.New(msg))
		return q
	} else {
		f.filterValue = valueVal
	}
	q.filters = append(q.filters, f)
	return q
}

func splitFilterString(filterString string) (fieldName string, operator string, err error) {
	split := strings.Split(filterString, " ")
	if len(split) != 2 {
		return "", "", errors.New("zoom: too many spaces in fieldStr argument. should be a field name, a space, and an operator.")
	}
	return split[0], split[1], nil
}

func (q *Query) filterById(operator string, value interface{}) *Query {
	if operator != "=" {
		q.setErrorIfNone(errors.New("zoom: only the = operator can be used with Filter on Id field."))
		return q
	}
	idVal := reflect.ValueOf(value)
	if idVal.Kind() != reflect.String {
		msg := fmt.Sprintf("zoom: for a Filter on Id field, value must be a string type. Was type %s", idVal.Kind().String())
		q.setErrorIfNone(errors.New(msg))
		return q
	}
	f := filter{
		fieldName:   "Id",
		redisName:   "Id",
		filterType:  equal,
		filterValue: idVal,
		byId:        true,
	}
	q.filters = append(q.filters, f)
	return q
}

// Run is a query finisher. It runs the query and returns the results in
// the form of an interface. The true type of the return value will be
// a slice of pointers to some regestired model type. If you need a type-safe
// way to run queries, look at the Scan method. Any errors that were caused
// by invalid arguments to query modifiers will be returned here.
func (q *Query) Run() (interface{}, error) {
	if q.err != nil {
		return nil, q.err
	}

	ids, err := q.GetIds()
	if err != nil {
		return nil, err
	}

	// create a slice in which to store results using reflection the
	// type of the slice whill match the type of the model being queried
	resultsVal := reflect.New(reflect.SliceOf(q.modelSpec.modelType))
	resultsVal.Elem().Set(reflect.MakeSlice(reflect.SliceOf(q.modelSpec.modelType), 0, 0))

	if err := scanModelsByIds(resultsVal, q.modelSpec.modelName, ids, q.getIncludes()); err != nil {
		return resultsVal.Elem().Interface(), err
	}
	return resultsVal.Elem().Interface(), nil
}

// Scan is a query finisher. It runs the query and attempts
// to scan the results into in. The type of in should be a pointer
// to a slice of pointers to a registered model type. Any errors that
// were caused by invalid arguments to query modifiers will be returned
// here.
func (q *Query) Scan(in interface{}) error {
	if q.err != nil {
		return q.err
	}

	// make sure we are dealing with the right type
	typ := reflect.TypeOf(in).Elem()
	if !(typ.Kind() == reflect.Slice) {
		msg := fmt.Sprintf("zoom: Query.Scan requires a pointer to a slice or array as an argument. Got: %T", in)
		return errors.New(msg)
	}
	elemType := typ.Elem()
	if !typeIsPointerToStruct(elemType) {
		msg := fmt.Sprintf("zoom: Query.Scan requires a pointer to a slice of pointers to model structs. Got: %T", in)
		return errors.New(msg)
	}
	if elemType != q.modelSpec.modelType {
		msg := fmt.Sprintf("zoom: argument for Query.Scan did not match the type corresponding to the model name given in the NewQuery constructor.\nExpected %T but got %T", reflect.SliceOf(q.modelSpec.modelType), in)
		return errors.New(msg)
	}

	ids, err := q.GetIds()
	if err != nil {
		return err
	}

	resultsVal := reflect.ValueOf(in)
	resultsVal.Elem().Set(reflect.MakeSlice(reflect.SliceOf(q.modelSpec.modelType), 0, 0))

	return scanModelsByIds(resultsVal, q.modelSpec.modelName, ids, q.getIncludes())
}

// Count is a query finisher. It counts the number of models that would
// be returned by the query without actually running the query. Any errors
// that were caused by invalid arguments to query modifiers will be returned
// here. There is currently a bug where Count may not work correctly for queries
// with Filter modifiers.
func (q *Query) Count() (int, error) {
	if q.err != nil {
		return 0, q.err
	}
	return q.getIdCount()
}

// IdsOnly is a query finisher. It returns only the ids of the models
// and doesn't actually retrieve all of their fields. Any errors that were caused
// by invalid arguments to query modifiers will be returned here.
func (q *Query) IdsOnly() ([]string, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.GetIds()
}

func (q *Query) setErrorIfNone(e error) {
	if q.err == nil {
		q.err = e
	}
}

// getIncludes parses the includes and excludes properties to return
// a list of fieldNames which should be included in all find operations.
// a return value of nil means that all fields should be considered.
func (q *Query) getIncludes() []string {
	if len(q.includes) != 0 {
		return q.includes
	} else if len(q.excludes) != 0 {
		results := q.modelSpec.fieldNames
		for _, name := range q.excludes {
			results = removeElementFromStringSlice(results, name)
		}
		return results
	}
	return nil
}

// GetIds() executes a single command and returns the ids of every
// model which should be found for the query.
func (q *Query) GetIds() ([]string, error) {
	conn := GetConn()
	defer conn.Close()

	if len(q.filters) == 0 {
		return q.getIdsWithoutFilters()
	} else {
		return q.getIdsWithFilters()
	}

}

func (q *Query) getIdsWithoutFilters() ([]string, error) {
	conn := GetConn()
	defer conn.Close()

	if q.order.fieldName == "" {
		// without ordering
		if q.offset != 0 {
			return nil, errors.New("zoom: offset cannot be applied to queries without an order.")
		}
		indexKey := q.modelSpec.modelName + ":all"
		args := redis.Args{}.Add(indexKey)
		var command string
		if q.limit == 0 {
			command = "SMEMBERS"
		} else {
			command = "SRANDMEMBER"
			args = args.Add(q.limit)
		}
		return redis.Strings(conn.Do(command, args...))
	} else {
		// with ordering
		return q.getOrderedIds()
	}
}

func (q *Query) getIdsWithFilters() ([]string, error) {

	// get a set of ids for each filter
	idSets := map[string][]string{}
	for _, filter := range q.filters {
		ids, err := filter.getIds(q.modelSpec.modelName, q.order)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			// if any filter returns zero matching models, the query should return nothing
			return []string{}, nil
		}
		idSets[filter.fieldName] = ids
	}

	switch q.order.fieldName {
	case "":
		// no ordering
		idSetsSlice := [][]string{}
		for _, ids := range idSets {
			idSetsSlice = append(idSetsSlice, ids)
		}
		final := idSetsSlice[0]
		for _, ids := range idSetsSlice[0:] {
			final = orderedIntersectStrings(final, ids)
		}
		return final, nil
	default:
		// with ordering

		// if there is an order to the query, but no filter with that same fieldname, we
		// need to get an ordered list of ids to be used for sorting
		// TODO: optimize this by doing a manual sort (in go instead of in redis) if the
		// models we're dealing with are significantly smaller than the total number of
		// models
		// TODO: move this into a transaction with the other filters
		primaryIds, found := idSets[q.order.fieldName]
		if !found {
			orderedIds, err := q.getOrderedIds()
			if err != nil {
				return nil, err
			}
			primaryIds = orderedIds
		}

		final := []string{}
		final = append(final, primaryIds...)

		// intersect primary ids with all the other id sets
		// this ensures ordering by the primary ids, the ids which we wanted
		// to order the query by.
		for fieldName, oIds := range idSets {
			if fieldName != q.order.fieldName {
				final = orderedIntersectStrings(final, oIds)
			}
		}
		return final, nil
	}
}

// TODO: move this into a transaction
func (q Query) getOrderedIds() ([]string, error) {
	conn := GetConn()
	defer conn.Close()
	args := redis.Args{}
	var command string
	if q.order.orderType == ascending {
		command = "ZRANGE"
	} else if q.order.orderType == descending {
		command = "ZREVRANGE"
	}
	start := q.offset
	stop := -1
	if q.limit != 0 {
		stop = int(start) + int(q.limit) - 1
	}
	indexKey := q.modelSpec.modelName + ":" + q.order.redisName
	args = args.Add(indexKey).Add(start).Add(stop)
	if q.order.indexType == indexNumeric || q.order.indexType == indexBoolean {
		// ordered by numeric or boolean index
		// just return results of command directly
		return redis.Strings(conn.Do(command, args...))
	} else {
		// ordered by alpha index
		// we need to parse ids from the result of the command
		ids, err := redis.Strings(conn.Do(command, args...))
		if err != nil {
			return nil, err
		}
		for i, valueAndId := range ids {
			ids[i] = extractModelIdFromAlphaIndexValue(valueAndId)
		}
		return ids, nil
	}
}

// TODO: make queries with multiple filters use a single transaction
// for all of them
func (f filter) getIds(modelName string, o order) ([]string, error) {
	// special case for id filters
	if f.byId {
		id := f.filterValue.String()
		return []string{id}, nil
	} else {
		setKey := modelName + ":" + f.redisName
		reverse := o.orderType == descending && o.fieldName == f.fieldName
		switch f.indexType {

		case indexNumeric:
			args := redis.Args{}.Add(setKey)
			switch f.filterType {
			case equal, less, greater, lessOrEqual, greaterOrEqual:
				var min, max interface{}
				switch f.filterType {
				case equal:
					min, max = f.filterValue.Interface(), f.filterValue.Interface()
				case less:
					min = "-inf"
					// use "(" for exclusive
					max = fmt.Sprintf("(%v", f.filterValue.Interface())
				case greater:
					// use "(" for exclusive
					min = fmt.Sprintf("(%v", f.filterValue.Interface())
					max = "+inf"
				case lessOrEqual:
					min = "-inf"
					max = f.filterValue.Interface()
				case greaterOrEqual:
					min = f.filterValue.Interface()
					max = "+inf"
				}
				// execute command to get the ids
				// TODO: try and do this inside of a transaction
				var command string
				if !reverse {
					command = "ZRANGEBYSCORE"
					args = args.Add(min).Add(max)
				} else {
					command = "ZREVRANGEBYSCORE"
					args = args.Add(max).Add(min)
				}
				conn := GetConn()
				defer conn.Close()
				idSlice, err := redis.Strings(conn.Do(command, args...))
				if err != nil {
					return nil, err
				}
				return idSlice, nil
			case notEqual:
				// special case for not equals
				// split into two different queries (less and greater) and
				// use union to combine the results
				t := newTransaction()
				max := fmt.Sprintf("(%v", f.filterValue.Interface())
				lessIds := []string{}
				lessArgs := args.Add("-inf").Add(max)
				if err := t.command("ZRANGEBYSCORE", lessArgs, newScanSliceHandler(&lessIds)); err != nil {
					return nil, err
				}
				min := fmt.Sprintf("(%v", f.filterValue.Interface())
				greaterIds := []string{}
				greaterArgs := args.Add(min).Add("+inf")
				if err := t.command("ZRANGEBYSCORE", greaterArgs, newScanSliceHandler(&greaterIds)); err != nil {
					return nil, err
				}

				// execute the transaction to scan in values for lessIds and greaterIds
				if err := t.exec(); err != nil {
					return nil, err
				}
				if !reverse {
					return append(lessIds, greaterIds...), nil
				} else {
					for i, j := 0, len(lessIds)-1; i <= j; i, j = i+1, j-1 {
						lessIds[i], lessIds[j] = lessIds[j], lessIds[i]
					}
					for i, j := 0, len(greaterIds)-1; i <= j; i, j = i+1, j-1 {
						greaterIds[i], greaterIds[j] = greaterIds[j], greaterIds[i]
					}
					return append(greaterIds, lessIds...), nil
				}
			}

		case indexBoolean:
			args := redis.Args{}.Add(setKey)
			var min, max interface{}
			switch f.filterType {
			case equal:
				if f.filterValue.Bool() == true {
					// use 1 for true
					min, max = 1, 1
				} else {
					// use 0 for false
					min, max = 0, 0
				}
			case less:
				if f.filterValue.Bool() == true {
					// false is less than true
					// 0 < 1
					min, max = 0, 0
				} else {
					// can't be less than false (0)
					return []string{}, nil
				}
			case greater:
				if f.filterValue.Bool() == true {
					// can't be greater than true (1)
					return []string{}, nil
				} else {
					// true is greater than false
					// 1 > 0
					min, max = 1, 1
				}
			case lessOrEqual:
				if f.filterValue.Bool() == true {
					// true and false are <= true
					// 1 <= 1 and 0 <= 1
					min = 0
					max = 1
				} else {
					// false <= false
					// 0 <= 0
					min, max = 0, 0
				}
			case greaterOrEqual:
				if f.filterValue.Bool() == true {
					// true >= true
					// 1 >= 1
					min, max = 1, 1
				} else {
					// false and true are >= false
					// 0 >= 0 and 1 >= 0
					min = 0
					max = 1
				}
			case notEqual:
				if f.filterValue.Bool() == true {
					// not true means false
					// false == 0
					min, max = 0, 0
				} else {
					// not false means true
					// true == 1
					min, max = 1, 1
				}
			default:
				msg := fmt.Sprintf("zoom: Filter operator out of range. Got: %d", f.filterType)
				return nil, errors.New(msg)
			}
			// execute command to get the ids
			// TODO: try and do this inside of a transaction
			var command string
			reverse := o.orderType == descending && o.fieldName == f.fieldName
			if !reverse {
				command = "ZRANGEBYSCORE"
				args = args.Add(min).Add(max)
			} else {
				command = "ZREVRANGEBYSCORE"
				args = args.Add(max).Add(min)
			}
			conn := GetConn()
			defer conn.Close()
			idSlice, err := redis.Strings(conn.Do(command, args...))
			if err != nil {
				return nil, err
			}
			return idSlice, nil

		case indexAlpha:
			reverse := o.orderType == descending && o.fieldName == f.fieldName
			conn := GetConn()
			defer conn.Close()
			beforeRank, afterRank, err := f.getAlphaRanks(conn, setKey)
			if err != nil {
				return nil, err
			}
			args := redis.Args{}.Add(setKey)
			var start, stop int
			switch f.filterType {
			case equal, less, greater, lessOrEqual, greaterOrEqual:
				switch f.filterType {
				case equal:
					if beforeRank+1 == afterRank {
						// no models with value equal to target
						return []string{}, nil
					}
					start = beforeRank
					stop = afterRank - 2
				case less:
					if afterRank <= 2 {
						// no models with value less than target
						return []string{}, nil
					}
					start = 0
					stop = afterRank - (afterRank - beforeRank) - 1
				case greater:
					start = beforeRank + (afterRank - beforeRank) - 1
					stop = -1
				case lessOrEqual:
					if afterRank <= 1 {
						// no models with value less than or equal to target
						return []string{}, nil
					}
					start = 0
					stop = afterRank - 2
				case greaterOrEqual:
					start = beforeRank
					stop = -1
				}
				args = args.Add(start).Add(stop)
				ids, err := redis.Strings(conn.Do("ZRANGE", args...))
				if err != nil {
					return nil, err
				}
				for i, valueAndId := range ids {
					ids[i] = extractModelIdFromAlphaIndexValue(valueAndId)
				}
				if reverse {
					for i, j := 0, len(ids)-1; i <= j; i, j = i+1, j-1 {
						ids[i], ids[j] = ids[j], ids[i]
					}
				}
				return ids, nil
			case notEqual:
				// special case for not equals
				// split into two different queries (less and greater) and
				// use union to combine the results
				t := newTransaction()
				lessIds := []string{}
				if afterRank > 3 {
					lessArgs := args.Add(0).Add(afterRank - (afterRank - beforeRank) - 1)
					if err := t.command("ZRANGE", lessArgs, newScanSliceHandler(&lessIds)); err != nil {
						return nil, err
					}
				}
				greaterArgs := args.Add(beforeRank + (afterRank - beforeRank) - 1).Add(-1)
				greaterIds := []string{}
				if err := t.command("ZRANGE", greaterArgs, newScanSliceHandler(&greaterIds)); err != nil {
					return nil, err
				}
				// execute the transaction to scan in values for lessIds and greaterIds
				if err := t.exec(); err != nil {
					return nil, err
				}
				for i, valueAndId := range lessIds {
					lessIds[i] = extractModelIdFromAlphaIndexValue(valueAndId)
				}
				for i, valueAndId := range greaterIds {
					greaterIds[i] = extractModelIdFromAlphaIndexValue(valueAndId)
				}
				if !reverse {
					return append(lessIds, greaterIds...), nil
				} else {
					for i, j := 0, len(lessIds)-1; i <= j; i, j = i+1, j-1 {
						lessIds[i], lessIds[j] = lessIds[j], lessIds[i]
					}
					for i, j := 0, len(greaterIds)-1; i <= j; i, j = i+1, j-1 {
						greaterIds[i], greaterIds[j] = greaterIds[j], greaterIds[i]
					}
					return append(greaterIds, lessIds...), nil
				}

			}

		default:
			msg := fmt.Sprintf("zoom: cannot use filters on unindexed field %s for model name %s.", f.fieldName, modelName)
			return nil, errors.New(msg)
		}
	}
	return nil, nil
}

func (f filter) getAlphaRanks(conn redis.Conn, setKey string) (beforeRank int, afterRank int, err error) {
	// TODO: try and do this inside of a single transaction
	// or (better yet) implement some server-side lua
	t := newTransaction()
	target := f.filterValue.String()
	// add a value to the sorted set which is garunteed to come before the
	// target value when sorted
	before := target
	baseArgs := redis.Args{}.Add(setKey)
	beforeArgs := baseArgs.Add(0).Add(before)
	if err := t.command("ZADD", beforeArgs, nil); err != nil {
		return 0, 0, err
	}
	// add a value to the sorted set which is garunteed to come after the
	// target value when sorted
	after := target + delString
	afterArgs := baseArgs.Add(0).Add(after)
	if err := t.command("ZADD", afterArgs, nil); err != nil {
		return 0, 0, err
	}
	// get the rank of the value inserted before and remember it
	beforeRankArgs := baseArgs.Add(before)
	if err := t.command("ZRANK", beforeRankArgs, newSingleScanHandler(&beforeRank)); err != nil {
		return 0, 0, err
	}
	// get the rank of the value inserted after and remember it
	afterRankArgs := baseArgs.Add(after)
	if err := t.command("ZRANK", afterRankArgs, newSingleScanHandler(&afterRank)); err != nil {
		return 0, 0, err
	}
	// now remove both of the values we had added
	removeArgs := baseArgs.Add(before).Add(after)
	if err := t.command("ZREM", removeArgs, nil); err != nil {
		return 0, 0, err
	}
	// execute the transaction to scan in the values for beforeRank and afterRank
	if err := t.exec(); err != nil {
		return 0, 0, err
	}

	return beforeRank, afterRank, nil
}

// Alpha indexes are stored as "<fieldValue> <modelId>", so we need to
// extract the modelId. While fieldValue may have a space, modelId CANNOT
// have a space in it, so we can simply take the part of the stored value
// after the last space.
func extractModelIdFromAlphaIndexValue(valueAndId string) string {
	slices := strings.Split(valueAndId, " ")
	return slices[len(slices)-1]
}

// NOTE: sliceVal should be the value of a pointer to a slice of pointer to models.
// It's type should be *[]*<T>, where <T> is some type which satisfies the Model
// interface. The type *[]*Model is not equivalent and will not work.
func scanModelsByIds(sliceVal reflect.Value, modelName string, ids []string, includes []string) error {
	t := newTransaction()
	for _, id := range ids {
		mr, err := newModelRefFromName(modelName)
		if err != nil {
			return err
		}
		mr.model.SetId(id)
		if err := t.findModel(mr, includes); err != nil {
			if _, ok := err.(*KeyNotFoundError); ok {
				continue // key not found errors are fine
				// TODO: update the index in this case? Or maybe if it keeps happening?
			}
		}
		sliceVal.Elem().Set(reflect.Append(sliceVal.Elem(), mr.modelVal()))
	}
	return t.exec()
}

// getIdCount returns the number of models that would be found if the
// query were executed, but does not actually find them.
func (q *Query) getIdCount() (int, error) {
	conn := GetConn()
	defer conn.Close()

	args := redis.Args{}
	var command string
	if q.order.fieldName == "" {
		// without ordering
		if q.offset != 0 {
			return 0, errors.New("zoom: offset cannot be applied to queries without an order.")
		}
		command = "SCARD"
		indexKey := q.modelSpec.modelName + ":all"
		args = args.Add(indexKey)
		count, err := redis.Int(conn.Do("SCARD", args...))
		if err != nil {
			return 0, err
		}
		if q.limit == 0 {
			// limit of 0 is the same as unlimited
			return count, nil
		} else {
			limitInt := int(q.limit)
			if count > limitInt {
				return limitInt, nil
			} else {
				return count, nil
			}
		}
	} else {
		// with ordering
		// this is a little more complicated
		command = "ZCARD"
		indexKey := q.modelSpec.modelName + ":" + q.order.redisName
		args = args.Add(indexKey)
		count, err := redis.Int(conn.Do(command, args...))
		if err != nil {
			return 0, err
		}
		if q.limit == 0 && q.offset == 0 {
			// simple case (no limit, no offset)
			return count, nil
		} else {
			// we need to take limit and offset into account
			// in order to return the correct number of models which
			// would have been returned by running the query
			if q.offset > uint(count) {
				// special case for offset > count
				return 0, nil
			} else if q.limit == 0 {
				// special case if limit = 0 (really means unlimited)
				return count - int(q.offset), nil
			} else {
				// holy type coercion, batman!
				// it's ugly but it works
				return int(math.Min(float64(count-int(q.offset)), float64(q.limit))), nil
			}
		}
	}
}

// string returns a string representation of the filterType
func (ft filterType) string() string {
	switch ft {
	case equal:
		return "="
	case notEqual:
		return "!="
	case greater:
		return ">"
	case less:
		return "<"
	case greaterOrEqual:
		return ">="
	case lessOrEqual:
		return "<="
	}
	return ""
}

// string returns a string representation of the filter
func (f filter) string() string {
	return fmt.Sprintf("(filter %s %s %v)", f.fieldName, f.filterType.string(), f.filterValue.Interface())
}

// string returns a string representation of the order
func (o order) string() string {
	if o.fieldName == "" {
		return ""
	}
	switch o.orderType {
	case ascending:
		return fmt.Sprintf("(order %s)", o.fieldName)
	case descending:
		return fmt.Sprintf("(order -%s)", o.fieldName)
	}
	return ""
}

// String returns a string representation of the query and its modifiers
func (q *Query) String() string {
	modelName := q.modelSpec.modelName
	filters := ""
	for _, f := range q.filters {
		filters += f.string() + " "
	}
	order := q.order.string()
	limit := ""
	offset := ""
	if q.limit != 0 {
		limit = fmt.Sprintf("(limit %v)", q.limit)
	}
	if q.offset != 0 {
		offset = fmt.Sprintf("(offset %v)", q.offset)
	}
	includes := ""
	if len(q.includes) > 0 {
		fields := "["
		for i, in := range q.includes {
			fields += in
			if i != len(q.includes)-1 {
				fields += ", "
			}
		}
		fields += "]"
		includes = fmt.Sprintf("(include %s)", fields)
	}
	excludes := ""
	if len(q.excludes) > 0 {
		fields := "["
		for i, ex := range q.excludes {
			fields += ex
			if i != len(q.excludes)-1 {
				fields += ", "
			}
		}
		fields += "]"
		excludes = fmt.Sprintf("(exclude %s)", fields)
	}
	return fmt.Sprintf("%s: %s%s %s %s %s%s", modelName, filters, order, limit, offset, includes, excludes)
}
