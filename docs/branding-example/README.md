# Branding example

A working manifest, stylesheet, and logo. Copy the directory, edit
`branding.yaml`, and point the library at it.

```sh
DEMARKUS_BRANDING=$PWD/branding.yaml demarkus-library
```

In Kubernetes the same files become one ConfigMap:

```sh
kubectl create configmap library-branding \
  --from-file=branding.yaml --from-file=site.css \
  --from-file=notes.css --from-file=logo.svg
```

Then set `library.branding.configMap` and `library.branding.manifestKey` in
the chart's values. The full reference is [../theming.md](../theming.md).
