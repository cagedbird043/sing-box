### Structure

```json
{
  "type": "selector",
  "tag": "select",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "providers": ["subscription"],
  "use_all_providers": false,
  "include": "",
  "exclude": "",
  "default": "proxy-c",
  "interrupt_exist_connections": false
}
```

!!! quote ""

    The selector can only be controlled through the [Clash API](/configuration/experimental#clash-api-fields) currently.

### Fields

#### outbounds

At least one outbound or provider is required.

List of outbound tags to select.

#### providers

List of [outbound provider](/configuration/provider/) tags. Members update when a provider refreshes.

#### use_all_providers

Include all configured outbound providers.

#### include / exclude

Regular expressions filtering provider member tags (`provider/tag`). Exclusion takes precedence. Static `outbounds` are not filtered.

An empty provider group uses the direct `Compatible` outbound.


#### default

The default outbound tag. The first outbound will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
