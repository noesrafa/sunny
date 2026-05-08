# Persona files

Your "voice" — the soul block of your system prompt — comes from one or both of:

- **`prompt.md`** (single file): the simple form. One file = your whole persona.
- **`prompt/<NN>-name.md`** (multi-file, opt-in): split your persona into priority-ordered fragments.

Both can coexist. They're merged at turn start, sorted by priority ascending, then by filename. `prompt.md` is treated as priority 50.

## When to split

Use `prompt/` when your persona grows past one section. Common layout:

```
prompt/
  10-soul.md           # who you are, tone, core values
  30-instructions.md   # how to handle requests
  70-style.md          # output formatting preferences
```

`10-soul.md` lands first because lower numbers come first. A file without a numeric prefix gets priority 50.

## Disabling a fragment

Prefix the filename with `_` to skip it without deleting:

```
prompt/
  10-soul.md
  _30-old-instructions.md   # ignored
  30-instructions.md
```

Same convention as skills/knowledge: `_`-prefixed files are read by humans, not the daemon.

## Editing live

The daemon re-reads these files at the start of every turn. Save the file, send a new message, and the next turn picks up the change. No restart.
