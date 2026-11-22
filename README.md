# WhatsApp MCP Server

A Go binary with separate gateway and worker roles that serves one WhatsApp account through Model Context Protocol (MCP). The MCP client authenticates with a shared bearer token over streamable HTTP. WhatsApp connects through whatsmeow as a linked device.

> *Caution:* as with many MCP servers, the WhatsApp MCP is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). This means that prompt injection could lead to private data exfiltration.

## Architecture

- **Gateway.** Publishes the MCP and media HTTP endpoints, authenticates the calling app, and forwards requests to the worker.
- **Worker.** Owns the WhatsApp linked device and its database. It discovers chats and buffers live messages for selected conversations in RAM.

## Build, deploy, and use

See [BUILD.md](BUILD.md) for the Docker Compose quickstart, native and systemd deployment, and a walkthrough of pairing, subscriptions, sending messages, and media transfer.
