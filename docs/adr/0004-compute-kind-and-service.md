---
status: accepted
date: 2026-05-30
issue: "#15"
---

# Compute is a host runtime (`kind`); services are workloads hosted on it

We split "the host" from "what runs on it": a **compute** is a host runtime with a `kind` (`vm` now,
`cluster`/k8s reserved), and a **service** is a separate resource hosted on a compute via a `host`
foreign key. On a `vm` host a service's delivery `type` is `raw` (built now) or `container`
(reserved). Modelling these as distinct resources — rather than baking workloads into the compute
spec — keeps the host lifecycle independent of the things it runs and leaves room for k8s clusters and
container delivery without reshaping existing resources. Only `kind=vm` + `type=raw` are implemented
this phase; the other variants are accepted by the schema but flagged unimplemented.
