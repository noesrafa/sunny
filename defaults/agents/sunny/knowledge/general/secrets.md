# How to manage sunny secrets (read this if asked to set up a provider)

API keys and provider config live at `~/.sunny/secrets.yaml`. The
file is mode 0600 and edited the same way you edit knowledge files —
read it with `view`, write the new content. The daemon re-reads it
on every turn, so rotating an existing key takes effect immediately;
adding a brand-new provider section needs `sunny stop && sunny start`
so the engine picks up the new driver.

## File shape

```yaml
anthropic:
  api_key: sk-ant-…
openai:
  api_key: sk-…
ollama:
  api_key: …
  base_url: https://ollama.com   # optional; defaults to ollama.com
```

Top-level key = provider name. Each provider's value is a flat
map of field names → values. Empty maps are dropped on save.

## Supported providers

| Provider    | Required fields    | Optional fields | Where to get a key                        |
|-------------|--------------------|-----------------|--------------------------------------------|
| `anthropic` | `api_key`          | —               | https://console.anthropic.com/settings/keys |
| `openai`    | `api_key`          | —               | https://platform.openai.com/api-keys        |
| `ollama`    | `api_key`          | `base_url`      | https://ollama.com/settings/keys            |

The canonical list lives in `internal/secrets/catalog.go` and is
exposed at `GET /secrets/catalog` if you need it programmatically.

## Editing flow

1. Read the file: `view ~/.sunny/secrets.yaml`.
2. Add or update the provider section in YAML.
3. Write it back. Mode 0600 is preserved; the file already has it.
4. If you added a section that wasn't there before, suggest the
   user run `sunny stop && sunny start`. Otherwise the change is
   live on the next turn.

## Environment variables

`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OLLAMA_API_KEY` override the
file when both are present. Useful for CI/docker contexts where
secrets shouldn't be persisted to disk.

## What NOT to do

- Don't print key values back to the user in plain text after
  editing — confirm by structure ("anthropic.api_key set"), not by
  echoing the value.
- Don't commit `secrets.yaml` to git. It's in `.gitignore` for the
  sunny repo itself; user repos won't have that protection.
- Don't escalate to sudo to fix the file's permissions. Mode 0600
  is set by sunny on first write; if it's broken, ask the user to
  `chmod 600 ~/.sunny/secrets.yaml` themselves.
