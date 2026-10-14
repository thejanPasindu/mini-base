package main

import (
	"github.com/mini-base/mini-base/internal/kernel"
	"github.com/mini-base/mini-base/internal/modules/admin"
	"github.com/mini-base/mini-base/internal/modules/auth"
	"github.com/mini-base/mini-base/internal/modules/database"
	"github.com/mini-base/mini-base/internal/modules/example"
)

// registry is the composition root: the only place that knows every module.
// Adding a module = one line here. Whether it runs is decided at startup by
// MINIBASE_MODULES.
func registry() *kernel.Registry {
	r := kernel.NewRegistry()
	r.MustRegister(
		admin.New(),
		auth.New(),
		database.New(),
		example.New(),
	)
	return r
}
