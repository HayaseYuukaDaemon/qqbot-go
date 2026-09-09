package utils

import (
	"errors"
	"fmt"
	"maps"
	"sync"
)

type SafeMap[KeyT comparable, ValT any] struct {
	mu sync.RWMutex
	m  map[KeyT]ValT
}

func (s *SafeMap[KeyT, ValT]) Set(k KeyT, v ValT) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
}

func (s *SafeMap[KeyT, ValT]) Snapshot() map[KeyT]ValT {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[KeyT]ValT, len(s.m))
	maps.Copy(cp, s.m)
	return cp
}

func (s *SafeMap[KeyT, ValT]) Delete(k KeyT) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
}

func (s *SafeMap[KeyT, ValT]) Has(k KeyT) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.m[k]
	return ok
}

func (s *SafeMap[KeyT, ValT]) Get(k KeyT) (ValT, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[k]
	return v, ok
}

func (s *SafeMap[KeyT, ValT]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

func NewSafeMap[KeyT comparable, ValT any]() SafeMap[KeyT, ValT] {
	return SafeMap[KeyT, ValT]{mu: sync.RWMutex{}, m: make(map[KeyT]ValT)}
}

type ErrorBag struct {
	m SafeMap[string, error]
}

type Results[KeyT comparable, ValT any] struct {
	em SafeMap[KeyT, error]
	rm SafeMap[KeyT, ValT]
	mu sync.RWMutex
}

func NewResult[KeyT comparable, ValT any]() Results[KeyT, ValT] {
	return Results[KeyT, ValT]{mu: sync.RWMutex{}, em: NewSafeMap[KeyT, error](), rm: NewSafeMap[KeyT, ValT]()}
}

func (r *Results[KeyT, ValT]) SetResult(k KeyT, v ValT) {
	r.em.Delete(k)
	r.rm.Set(k, v)
}

func (r *Results[KeyT, ValT]) SetError(k KeyT, err error) {
	r.rm.Delete(k)
	r.em.Set(k, err)
}

func (r *Results[KeyT, ValT]) CollectResults() map[KeyT]ValT {
	return r.rm.Snapshot()
}

func (r *Results[KeyT, ValT]) CollectErrors() map[KeyT]error {
	return r.em.Snapshot()
}

func (r *Results[KeyT, ValT]) PackErrors() error {
	if r.em.Len() <= 0 {
		return nil
	}
	m := r.em.Snapshot()
	var errs []error
	for name, err := range m {
		if err == nil {
			continue
		}
		errs = append(errs, fmt.Errorf("%v:%w", name, err))
	}
	if len(errs) <= 0 {
		return nil
	}
	return errors.Join(errs...)
}

func NewErrorBag() *ErrorBag {
	return &ErrorBag{m: NewSafeMap[string, error]()}
}

func (b *ErrorBag) SetError(name string, err error) {
	b.m.Set(name, err)
}

func (b *ErrorBag) GetError(name string, err error) (error, bool) {
	return b.m.Get(name)
}

func (b *ErrorBag) Snapshot() map[string]error {
	return b.m.Snapshot()
}

func (b *ErrorBag) PackError() error {
	if b.m.Len() <= 0 {
		return nil
	}
	m := b.m.Snapshot()
	var errs []error
	for name, err := range m {
		if err == nil {
			continue
		}
		errs = append(errs, fmt.Errorf("%s:%w", name, err))
	}
	if len(errs) <= 0 {
		return nil
	}
	return errors.Join(errs...)
}
