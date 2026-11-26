// Package module is what the server knows about the agent's modules (KONZEPT §15, ADR 0039):
// their schemas (copied from the agent by `just gen`), the subset of JSON Schema the agent checks
// configurations against, and the configuration, health and discovery hints of the nodes. Pure,
// without I/O.
package module
