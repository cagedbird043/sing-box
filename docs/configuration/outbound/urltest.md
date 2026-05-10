### Structure

```json
{
  "type": "urltest",
  "tag": "auto",
  
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
  "url": "",
  "interval": "",
  "tolerance": 0,
  "idle_timeout": "",
  "interrupt_exist_connections": false
}
```

### Fields

#### outbounds

List of outbound tags to test.

Either `outbounds`, `providers`, or `use_all_providers` must provide at least one test member.

#### providers

List of [Provider](/configuration/provider/) tags to include in the URLTest group.

Provider member tags are generated as `<provider-tag>/<member-tag>`.

#### exclude

A regular expression. Provider-generated outbound tags matching this expression are excluded from URL tests.

#### include

A regular expression. If non-empty, only provider-generated outbound tags matching this expression are included in URL tests.

#### use_all_providers

Include all configured providers in the URLTest group.

When enabled, `providers` is replaced with the current top-level provider list at startup.

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### tolerance

The test tolerance in milliseconds. `50` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
