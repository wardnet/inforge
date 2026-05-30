# Resources are partitioned by environment then region; commands act on one environment

Resource definitions live under `resources/<env>/<region>/<type>/`, and every inforge command takes
a single `<env>` and never fans out across environments. We deliberately dropped the TypeScript
toolkit's "walk every environment" behaviour: acting on one environment per invocation makes the
blast radius explicit and matches how operators reason about prd vs dev. Regions remain plural within
an environment because a single environment legitimately deploys to several.
