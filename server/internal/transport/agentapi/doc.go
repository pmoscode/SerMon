// Package agentapi implements sermon.agent.v1.AgentService – the mTLS gRPC endpoint agents
// connect to (M1-10). Every incoming message is validated against the buf.validate rules of
// the schema before it reaches the app layer; identity comes only from the client certificate.
package agentapi
