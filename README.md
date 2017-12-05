## configmaster

This is a small daemon that runs in a kubernetes cluster and watches for changes to ConfigMap and Secret resources, triggering a rolling update
of Deployment resources that refer to them after a delay (in order to debounce multiple changes).

### Configuration

Settings are all passed through environment variables

- `CONFIGMASTER_NAMESPACE`: The namespace to monitor, defaults to `default`
- `CONFIGMASTER_DELAY`: The delay, in seconds, to wait after detecting a change in resource before adding a Deployment patch to the queue. The same delay applies before applying the patch to the Deployment itself. In effect, this value * 2 is how long it will take before a Deployment is actually updated. Defaults to `5`
- `CONFIGMASTER_HOST`: The host to connect to (may contain port as well), for example `127.0.0.1:8001`. Leave this unset when running inside a cluster, this setting is primarily to allow development while using `kubectl proxy`
