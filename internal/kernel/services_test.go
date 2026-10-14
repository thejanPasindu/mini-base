package kernel_test

import (
	"testing"

	"github.com/mini-base/mini-base/internal/kernel"
)

type greeter interface{ Greet() string }

type english struct{}

func (english) Greet() string { return "hello" }

func TestServicesProvideAndLookup(t *testing.T) {
	t.Parallel()

	s := kernel.NewServices()
	if err := s.Provide("example.greeter", english{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Provide("example.greeter", english{}); err == nil {
		t.Fatal("duplicate provide accepted")
	}

	g, ok, err := kernel.Lookup[greeter](s, "example.greeter")
	if err != nil || !ok || g.Greet() != "hello" {
		t.Fatalf("lookup = %v, %v, %v", g, ok, err)
	}

	_, ok, err = kernel.Lookup[greeter](s, "missing")
	if ok || err != nil {
		t.Fatal("absent service should be ok=false without error")
	}

	_, _, err = kernel.Lookup[interface{ Other() }](s, "example.greeter")
	if err == nil {
		t.Fatal("wrong type should be an error")
	}
}
