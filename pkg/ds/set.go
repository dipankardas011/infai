package ds

import "sync"

// For the Set datastructure

type Set[T comparable] struct {
	mu       *sync.RWMutex
	elements map[T]struct{}
}

// NewSet creates and returns a new set.
func NewSet[T comparable]() *Set[T] {
	return &Set[T]{
		elements: make(map[T]struct{}),
		mu:       &sync.RWMutex{},
	}
}

func (s *Set[T]) Add(element T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.elements[element] = struct{}{}
}

func (s *Set[T]) Remove(element T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.elements, element)
}

func (s *Set[T]) Exists(element T) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.elements[element]
	return exists
}

func (s *Set[T]) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.elements)
}

func (s *Set[T]) ToSlice() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]T, 0, len(s.elements))
	for key := range s.elements {
		keys = append(keys, key)
	}
	return keys
}

// func (s *Set[T]) Union(other *Set[T]) *Set[T] {
// 	result := NewSet[T]()
// 	for key := range s.elements {
// 		result.Add(key)
// 	}
// 	for key := range other.elements {
// 		result.Add(key)
// 	}
// 	return result
// }
//
// func (s *Set[T]) Intersection(other *Set[T]) *Set[T] {
// 	result := NewSet[T]()
// 	for key := range s.elements {
// 		if other.Contains(key) {
// 			result.Add(key)
// 		}
// 	}
// 	return result
// }
//
// func (s *Set[T]) Difference(other *Set[T]) *Set[T] {
// 	result := NewSet[T]()
// 	for key := range s.elements {
// 		if !other.Contains(key) {
// 			result.Add(key)
// 		}
// 	}
// 	return result
// }
