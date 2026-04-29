package translate

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeTranslator is the minimal Translator stand-in for Registry tests.
// Translate is never called in the tests below; we only need Kind().
type fakeTranslator struct{ kind string }

func (f *fakeTranslator) Kind() string { return f.kind }
func (f *fakeTranslator) Translate(ctx context.Context, in Inputs) (*Result, error) {
	return nil, errors.New("not implemented")
}

func TestRegistry_LookupHit(t *testing.T) {
	r := NewRegistry()
	tr := &fakeTranslator{kind: "cmake"}
	if err := r.Register(tr); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Lookup("cmake")
	if !ok {
		t.Fatal("Lookup(cmake) miss after Register")
	}
	if got != tr {
		t.Errorf("Lookup returned a different translator")
	}
}

func TestRegistry_LookupMiss(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("autotools"); ok {
		t.Errorf("Lookup on empty registry should miss")
	}
}

func TestRegistry_DuplicateRegisterErrors(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&fakeTranslator{kind: "cmake"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&fakeTranslator{kind: "cmake"}); err == nil {
		t.Errorf("expected error on duplicate kind, got nil")
	}
}

func TestRegistry_KindsSorted(t *testing.T) {
	r := NewRegistry()
	for _, k := range []string{"meson", "cmake", "autotools", "stack"} {
		if err := r.Register(&fakeTranslator{kind: k}); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Kinds()
	want := []string{"autotools", "cmake", "meson", "stack"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Kinds() = %v, want %v", got, want)
	}
}
