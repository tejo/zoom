// Copyright 2014 Alex Browne.  All rights reserved.
// Use of this source code is governed by the MIT
// license, which can be found in the LICENSE file.

// File util.go contains miscellaneous utility functions used throughout
// the zoom library. They are not intended for external use.

package zoom

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
)

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func indexOfStringSlice(a string, list []string) int {
	for i, b := range list {
		if b == a {
			return i
		}
	}
	return -1
}

func indexOfSlice(a interface{}, list interface{}) int {
	lVal := reflect.ValueOf(list)
	size := lVal.Len()
	for i := 0; i < size; i++ {
		elem := lVal.Index(i)
		if reflect.DeepEqual(a, elem.Interface()) {
			return i
		}
	}
	return -1
}

func stringSliceContains(a string, list []string) bool {
	return indexOfStringSlice(a, list) != -1
}

func sliceContains(a interface{}, list interface{}) bool {
	return indexOfSlice(a, list) != -1
}

func removeFromStringSlice(list []string, i int) []string {
	return append(list[:i], list[i+1:]...)
}

func removeElementFromStringSlice(list []string, elem string) []string {
	for i, e := range list {
		if e == elem {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func compareAsStringSet(expecteds, gots []string) (bool, string) {
	// make sure everything in expecteds is also in gots
	for _, e := range expecteds {
		index := indexOfStringSlice(e, gots)
		if index == -1 {
			msg := fmt.Sprintf("Missing expected element: %v", e)
			return false, msg
		}
	}

	// make sure everything in gots is also in expecteds
	for _, g := range gots {
		index := indexOfStringSlice(g, expecteds)
		if index == -1 {
			msg := fmt.Sprintf("Found extra element: %v", g)
			return false, msg
		}
	}

	return true, "ok"
}

func compareAsSet(expecteds, gots interface{}) (bool, string) {
	eVal := reflect.ValueOf(expecteds)
	gVal := reflect.ValueOf(gots)

	if !eVal.IsValid() {
		return false, "expecteds was nil"
	} else if !gVal.IsValid() {
		return false, "gots was nil"
	}

	// make sure everything in expecteds is also in gots
	for i := 0; i < eVal.Len(); i++ {
		expected := eVal.Index(i).Interface()
		index := indexOfSlice(expected, gots)
		if index == -1 {
			msg := fmt.Sprintf("Missing expected element: %v", expected)
			return false, msg
		}
	}

	// make sure everything in gots is also in expecteds
	for i := 0; i < gVal.Len(); i++ {
		got := gVal.Index(i).Interface()
		index := indexOfSlice(got, expecteds)
		if index == -1 {
			msg := fmt.Sprintf("Found unexpected element: %v", got)
			return false, msg
		}
	}
	return true, "ok"
}

func typeIsSliceOrArray(typ reflect.Type) bool {
	k := typ.Kind()
	return (k == reflect.Slice || k == reflect.Array) && typ.Elem().Kind() != reflect.Uint8
}

func typeIsPointerToStruct(typ reflect.Type) bool {
	return typ.Kind() == reflect.Ptr && typ.Elem().Kind() == reflect.Struct
}

func typeIsString(typ reflect.Type) bool {
	k := typ.Kind()
	return k == reflect.String || ((k == reflect.Slice || k == reflect.Array) && typ.Elem().Kind() == reflect.Uint8)
}

func typeIsNumeric(typ reflect.Type) bool {
	k := typ.Kind()
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func typeIsBool(typ reflect.Type) bool {
	k := typ.Kind()
	return k == reflect.Bool
}

func typeIsPrimative(typ reflect.Type) bool {
	return typeIsString(typ) || typeIsNumeric(typ) || typeIsBool(typ)
}

// generate a random int from min to max (inclusively).
// I.e. to get either 1 or 0, use randInt(0,1)
func randInt(min int, max int) int {
	if !(max-min >= 1) {
		panic("invalid args. max must be at least one more than min")
	}
	return min + rand.Intn(max-min+1)
}

// looseEquals returns true if the two things are equal.
// equality is based on underlying value, so if the pointer addresses
// are different it doesn't matter. We use gob encoding for simplicity,
// assuming that if the gob representation of two things is the same,
// those two things can be considered equal. Differs from reflect.DeepEqual
// because of the indifference concerning pointer addresses.
func looseEquals(one, two interface{}) (bool, error) {
	oneBytes, err := json.Marshal(one)
	if err != nil {
		return false, err
	}
	twoBytes, err := json.Marshal(two)
	if err != nil {
		return false, err
	}

	return (string(oneBytes) == string(twoBytes)), nil
}

func convertNumericToFloat64(val reflect.Value) (float64, error) {
	switch val.Type().Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := val.Int()
		return float64(integer), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uinteger := val.Uint()
		return float64(uinteger), nil
	case reflect.Float32, reflect.Float64:
		return val.Float(), nil
	default:
		err := fmt.Errorf("zoom: attempt to call convertNumericToFloat64 on non-numeric type %s", val.Type().String())
		return 0.0, err
	}
}

func modelIds(ms []Model) []string {
	results := make([]string, len(ms))
	for i, m := range ms {
		results[i] = m.GetId()
	}
	return results
}

// converts a bool to an int using the following rule:
// false = 0
// true = 1
func boolToInt(b bool) int {
	if b {
		return 1
	} else {
		return 0
	}
}

// intersects two string slices. The order will be preserved
// with respect to the first slice. (The first slice is used
// in the outer loop). The return value is a copy, so neither
// the first or second slice will be mutated.
func orderedIntersectStrings(first []string, second []string) []string {
	results := make([]string, 0)
	memo := make(map[string]struct{})
	for _, a := range second {
		memo[a] = struct{}{}
	}
	for _, a := range first {
		if _, found := memo[a]; found {
			results = append(results, a)
		}
	}
	return results
}

// intersects two model slices. The order will be preserved
// with respect to the first slice. (The first slice is used
// in the outer loop). The return value is a copy, so neither
// the first or second slice will be mutated.
func orderedIntersectModels(first []*indexedPrimativesModel, second []*indexedPrimativesModel) []*indexedPrimativesModel {
	results := make([]*indexedPrimativesModel, 0)
	memo := make(map[*indexedPrimativesModel]struct{})
	for _, m := range second {
		memo[m] = struct{}{}
	}
	for _, m := range first {
		if _, found := memo[m]; found {
			results = append(results, m)
		}
	}
	return results
}
