import claudeCodeSh from '#configs/clients/claude-code.sh?raw'
import codexToml from '#configs/clients/codex.toml?raw'
import codexModels from '#configs/clients/codex-models.json?raw'
import crushJson from '#configs/clients/crush.json?raw'
import opencodeJsonc from '#configs/clients/opencode.jsonc?raw'
import * as write from './configs'
import { ClientRecipe } from './model'

/**
 * Every client type the gateway hands configs to, as data: its address rule,
 * its files and how each is written for one client. Shared by the public docs
 * page, which shows the files as committed, and a client's page, which writes
 * them with the client's own names.
 *
 * The config files themselves are not here — they are the committed ones under
 * configs/clients, imported as text, so there is one copy of each rather than a
 * second set pasted into a component. The types' slots and labels are not here
 * either: they are the server's (/admin/client-types).
 */
export const RECIPES: ClientRecipe[] = [
  new ClientRecipe({
    id: 'claude-code',
    versioned: false,
    lead: (
      <>
        Claude Code appends <code>/v1</code> to whatever base URL it is given, so the address here
        carries none. Source the file, or put its contents in your shell profile.
      </>
    ),
    files: [
      {
        label: 'claudication.sh',
        filename: 'claude-code.sh',
        lang: 'sh',
        body: claudeCodeSh,
        write: write.claudeCodeSh,
        // The file is the gateway's whole, key and all; an update rewrites
        // it keeping the key's line from the one on disk.
        install: { path: '.config/claudication/claude-code.sh', env: true, update: { keep: ['ANTHROPIC_AUTH_TOKEN'] } },
      },
    ],
  }),
  new ClientRecipe({
    id: 'codex',
    versioned: true,
    lead: (
      <>
        Codex speaks the OpenAI Responses API alone, which reaches both providers: a GPT model as
        Codex wrote the request, a Claude one translated. Two files: the config, and a model catalog
        Codex reads instead of asking.
      </>
    ),
    files: [
      {
        label: '~/.codex/config.toml',
        filename: 'config.toml',
        lang: 'toml',
        body: codexToml,
        write: write.codexToml,
        // Neither Codex file holds the key — config.toml names the variable
        // it is in, which no file here sets — so an update writes both as an
        // install does.
        install: { path: '.codex/config.toml', merge: { format: 'toml' } },
      },
      {
        label: '~/.codex/claudication-models.json',
        filename: 'claudication-models.json',
        lang: 'json',
        body: codexModels,
        write: write.codexCatalog,
        install: { path: '.codex/claudication-models.json' },
      },
    ],
    notes: [
      <>
        Codex needs <code>bubblewrap</code> installed before it can run any shell command. Without
        it the first tool call panics, and the model then explains — convincingly — that nothing
        works.
      </>,
      <>
        The catalog carries a short stand-in for Codex&rsquo;s own system prompt, because Codex
        refuses an entry without one and its real prompt lives inside its binary. The stand-in is
        about nine thousand tokens cheaper per turn, and less good at editing.{' '}
        <code>scripts/codex-model-catalog.py</code> clones the real one out of your own Codex if you
        would rather have that.
      </>,
    ],
  }),
  new ClientRecipe({
    id: 'opencode',
    versioned: true,
    lead: (
      <>
        This overrides opencode&rsquo;s built-in Anthropic provider rather than declaring a new one,
        and each name is declared with the limits of the model it serves. Its names carry{' '}
        <code>claude</code> whatever they serve: opencode streams tool calls on that provider only for
        such ids.
      </>
    ),
    files: [
      {
        label: '~/.config/opencode/opencode.jsonc',
        filename: 'opencode.jsonc',
        lang: 'jsonc',
        body: opencodeJsonc,
        write: (body, setup) => write.opencodeJsonc(body, setup),
        // The anthropic provider is the gateway's to own; the default models
        // are its too, since they name its slots. An openai override left by
        // a configuration from when opencode could be pointed at Responses
        // stays as it was (the merge only rewrites what the new file has): it
        // points at the gateway and names nothing the defaults use.
        install: {
          path: '.config/opencode/opencode.jsonc',
          merge: { format: 'json', owns: ['provider.anthropic', 'model', 'small_model'] },
          // An update owns the declared models and the two defaults, and
          // never the provider's options, where the key is.
          update: { strip: ['provider.anthropic.options.apiKey'], owns: ['provider.anthropic.models', 'model', 'small_model'] },
        },
      },
    ],
  }),
  new ClientRecipe({
    id: 'crush',
    versioned: false,
    lead: (
      <>
        Every name is spelled out here, and that is deliberate: crush is never asked to call{' '}
        <code>/v1/models</code> for a provider whose models are listed, so a model missing from
        this file cannot be selected however well the gateway serves it. Each is listed in
        crush&rsquo;s picker (<code>ctrl+l</code>) under its name and the model it serves, names
        added on the client&rsquo;s page included. The <code>large</code> and <code>small</code>{' '}
        picks are a first run&rsquo;s defaults: crush saves what you pick to its data file
        (<code>~/.local/share/crush/crush.json</code>), which wins over this one.
      </>
    ),
    files: [
      {
        label: '~/.config/crush/crush.json',
        filename: 'crush.json',
        lang: 'json',
        body: crushJson,
        write: write.crushJson,
        // ~/.config/crush/crush.json on every OS, Windows included: crush
        // reads its config from XDG_CONFIG_HOME or <home>/.config
        // (internal/home/home.go:26, config/load.go:1210 at 76cc5c57).
        // %LOCALAPPDATA%\crush is its data file, where the picker saves picks
        // (load.go:1266) — writing there reset them on every install. Its own
        // provider, and the large and small picks, which name it; the
        // operator's other providers stay.
        install: {
          path: '.config/crush/crush.json',
          merge: { format: 'json', owns: ['providers.claudication', 'models.large', 'models.small'] },
          // An update owns the provider's model list and the two picks, and
          // never its api_key.
          update: { strip: ['providers.claudication.api_key'], owns: ['providers.claudication.models', 'models.large', 'models.small'] },
          // Installs before 2026-10-22 wrote it there on Windows. Only the
          // provider is taken back: models.large and models.small in that
          // file are the user's own picks now, which crush reads over ours.
          stale: { windowsPath: 'AppData/Local/crush/crush.json', owns: ['providers.claudication'] },
        },
      },
    ],
  }),
]

/** A type's recipe, or none for a custom client. */
export function recipeFor(typeId: string): ClientRecipe | undefined {
  return RECIPES.find((r) => r.id === typeId)
}
