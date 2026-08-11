# Conventions

## The PACKAGE column

Write only the path below the module root. Drop the module name itself and any
leading `./`.

- `github.com/wangy/pi-go/web` → `web`
- `scratch/calc`, in module `scratch` → `calc`
- the module root itself → `.`

## Ordering

Failures are listed in the order the test runner reported them. Do not sort or
group them: the first failure is usually the one worth reading.
