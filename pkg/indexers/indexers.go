package indexers

import (
	"fmt"
	"reflect"

	"k8s.io/client-go/tools/cache"
)

// GetByKey fetches the given value from the generic store and converts it to the right type.
func GetByKey[T any](store cache.Store, key string) (*T, bool, error) {
	obj, exists, err := store.GetByKey(key)
	if err != nil {
		return nil, false, err
	}

	if !exists {
		return nil, false, nil
	}

	result, ok := obj.(*T)
	if !ok {
		return nil, false, &UnexpectedTypeError{Expected: reflect.TypeOf(result), Actual: reflect.TypeOf(obj)}
	}

	return result, true, nil
}

// List converts a list of generic items to a specific type.
//
// This function is intended to be used for values returned on cache.Store and cache.Indexer methods.
func List[T any](items []interface{}, err error) ([]*T, error) {
	if err != nil {
		return nil, err
	}

	result := make([]*T, len(items))

	for i := range items {
		obj, ok := items[i].(*T)
		if !ok {
			return nil, &UnexpectedTypeError{Expected: reflect.TypeOf(obj), Actual: reflect.TypeOf(items[i])}
		}

		result[i] = obj
	}

	return result, nil
}

// Gen creates a generic index function from one that expects a specific type.
func Gen[V any](indexFunc func(obj V) ([]string, error)) cache.IndexFunc {
	return func(obj interface{}) ([]string, error) {
		v, ok := obj.(V)
		if !ok {
			return nil, &UnexpectedTypeError{Expected: reflect.TypeOf(v), Actual: reflect.TypeOf(obj)}
		}

		return indexFunc(v)
	}
}

// UnexpectedTypeError is returned when an expected type did not match the actually returned type.
type UnexpectedTypeError struct {
	Expected reflect.Type
	Actual   reflect.Type
}

func (u *UnexpectedTypeError) Error() string {
	return fmt.Sprintf("expected type %v, got type %v", u.Expected, u.Actual)
}
