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
  "providers": [
    "sub"
  ],
  "exclude": "",
  "include": "",
  "use_all_providers": false,
  "default": "proxy-c",
  "interrupt_exist_connections": false
}
```

!!! quote ""

    The selector can only be controlled through the [Clash API](/configuration/experimental#clash-api-fields) currently.

### Fields

#### outbounds

List of outbound tags to select.

Either `outbounds`, `providers`, or `use_all_providers` must provide at least one selectable member.

#### providers

List of [Provider](/configuration/provider/) tags to include in the selector.

Provider member tags are generated as `<provider-tag>/<member-tag>`.

#### exclude

A regular expression. Provider-generated outbound tags matching this expression are excluded from the selector.

#### include

A regular expression. If non-empty, only provider-generated outbound tags matching this expression are included in the selector.

#### use_all_providers

Include all configured providers in the selector.

When enabled, `providers` is replaced with the current top-level provider list at startup.

#### default

The default outbound tag. The first outbound will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
