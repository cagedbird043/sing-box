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
  "providers": ["subscription"],
  "use_all_providers": false,
  "include": "",
  "exclude": "",
  "url": "",
  "interval": "",
  "tolerance": 0,
  "idle_timeout": "",
  "interrupt_exist_connections": false,
  "fallback": {"enabled": false, "max_delay": "1s"}
}
```

### Fields

#### outbounds

At least one outbound or provider is required.

List of outbound tags to test.

#### providers / use_all_providers / include / exclude

See [selector provider membership](/configuration/outbound/selector/#providers). Membership updates on provider refresh; empty groups use direct `Compatible`.

#### fallback

When `enabled`, prefer the first available member within `max_delay`. If every available member exceeds the limit, choose the fastest. An empty or zero limit accepts any delay.


#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

Probe history and automatic selection are scoped to the outbound and the effective test URL. Groups testing the same outbound against different URLs do not overwrite or invalidate each other's results. Empty URLs share the default URL's history. Nested URL-test groups retain their own configured targets.

Provider health checks and manual API tests share results only when their target URL matches. Group member delays use that group's effective target; a plain node's latest successful delay is a display view, not a fallback for target-specific selection. Replacing or removing provider nodes invalidates their history across all targets, and results from retired node generations are ignored.

When the current selection has no test result, the group selects a usable member as soon as its result is observed during testing, using the existing selection policy. It selects again from the complete results at the end of the round. Nested URL-test groups select early independently; the parent's selection of nested members is still determined at the end of its round.

#### interval

The test interval. `3m` will be used if empty.

#### tolerance

The test tolerance in milliseconds. `50` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
