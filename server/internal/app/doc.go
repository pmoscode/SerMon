// Package app contains the use cases and the authorization policy.
//
// It orchestrates the domain and talks to the outside world only through ports
// (interfaces) that are implemented in package adapters. Every data access is bound
// to a tenant scope (KONZEPT §7, §18).
package app
