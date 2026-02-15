// Package mcp implements MCP (Model Context Protocol) client support,
// allowing Thane to connect to external MCP servers and expose their
// tools to the agent loop and delegates.
//
// MCP uses JSON-RPC 2.0 over two transports: stdio (subprocess) and
// streamable HTTP. The client discovers tools via tools/list and invokes
// them via tools/call. Discovered tools are bridged into Thane's tool
// registry so they appear as native tools to the LLM.
//
// This implementation covers the client/host side only — Thane does not
// act as an MCP server.
package mcp
