// +build !dynamodb

// This file is needed for conditional compilation. It's used when
// the build tag 'dynamodb' is not defined. Otherwise the adapter.go
// is compiled.

package dynamodb
