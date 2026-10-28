---
icon: material/new-box
---

!!! question "Since sing-box 1.15.0-cagedbird"

# Race

### Structure

```json
{
  "type": "race",
  "tag": "edgex",
  "servers": ["edgex-1", "edgex-2"]
}
```

### Fields

#### servers

Required non-empty list of other DNS server tags. Any server type is supported, including nested race servers. Each server retains its own transport, TLS, detour and domain resolver settings. Missing tags, self-references and dependency cycles are rejected.

Race sends a separate copy of the query to every listed server concurrently. It accepts the first error-free NOERROR response containing at least one answer record of the requested type, then cancels the remaining exchanges. For A/AAAA questions, CNAME-only responses do not win.

If no response qualifies, race waits for all exchanges and returns an error-free response, preferring NXDOMAIN, then NOERROR without a matching answer, then other response codes. Ties use completion order. If all exchanges fail, their errors are joined. Cancellation of the caller cancels all exchanges.

Referenced servers are dependencies: the DNS transport manager starts them first and tracks them as referenced. Race does not start, close or reset their transports.
