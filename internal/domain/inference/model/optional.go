package model

// Optional preserves the distinction between an omitted value and an explicit zero value.
type Optional[T any] struct {
	Value T
	Set   bool
}

func None[T any]() Optional[T] { return Optional[T]{} }

func Some[T any](value T) Optional[T] { return Optional[T]{Value: value, Set: true} }
