import claudeCodeSh from '#configs/clients/claude-code.sh?raw'
import codexToml from '#configs/clients/codex.toml?raw'
import codexModels from '#configs/clients/codex-models.json?raw'
import crushJson from '#configs/clients/crush.json?raw'
import opencodeJsonc from '#configs/clients/opencode.jsonc?raw'
import {
  tailorClaudeCode,
  tailorCodexCatalog,
  tailorCodexConfig,
  tailorCrush,
  tailorOpencode,
} from './catalog'
import { ClientRecipe } from './model'

/**
 * Every client this gateway hands configs to, as data: its address rule, its
 * files and how each is tailored. Shared by the public docs page and a key's
 * Clients page, which is why it lives beside the catalog rather than in either.
 *
 * The config files themselves are not here — they are the committed ones under
 * configs/clients, imported as text, so there is one copy of each rather than a
 * second set pasted into a component.
 */
export const CLIENTS: ClientRecipe[] = [
  new ClientRecipe({
    id: 'claude-code',
    label: 'Claude Code',
    client: 'Claude Code',
    surface: 'anthropic',
    versioned: false,
    lead: (
      <>
        Claude Code appends <code>/v1</code> to whatever base URL it is
        given, so the address here carries none. Source the file, or put its contents in your
        shell profile.
      </>
    ),
    files: [
      {
        label: 'claudication.sh',
        filename: 'claude-code.sh',
        lang: 'sh',
        body: claudeCodeSh,
        tailor: tailorClaudeCode,
        install: { path: '.config/claudication/claude-code.sh', env: true },
      },
    ],
  }),
  new ClientRecipe({
    id: 'codex',
    label: 'Codex CLI',
    client: 'Codex',
    surface: 'openai',
    versioned: true,
    lead: (
      <>
        The only client here that goes through the OpenAI Responses API rather than the Anthropic
        one — so the gateway translates, and Codex never learns it is talking to Claude. Two files:
        the config, and a model catalog Codex reads instead of asking.
      </>
    ),
    files: [
      {
        label: '~/.codex/config.toml',
        filename: 'config.toml',
        lang: 'toml',
        body: codexToml,
        tailor: tailorCodexConfig,
        install: { path: '.codex/config.toml', merge: { format: 'toml' } },
      },
      {
        label: '~/.codex/claude-models.json',
        filename: 'claude-models.json',
        lang: 'json',
        body: codexModels,
        tailor: tailorCodexCatalog,
        install: { path: '.codex/claude-models.json' },
      },
    ],
    notes: [
      <>
        Codex needs <code>bubblewrap</code> installed before it can run any
        shell command. Without it the first tool call panics, and the model then explains —
        convincingly — that nothing works.
      </>,
      <>
        The catalog carries a short stand-in for Codex&rsquo;s own system prompt, because Codex
        refuses an entry without one and its real prompt lives inside its binary. The stand-in is
        about nine thousand tokens cheaper per turn, and less good at editing.{' '}
        <code>scripts/codex-model-catalog.py</code> clones the real one out
        of your own Codex if you would rather have that.
      </>,
    ],
  }),
  new ClientRecipe({
    id: 'opencode',
    label: 'opencode',
    client: 'opencode',
    surface: 'anthropic',
    versioned: true,
    lead: (
      <>
        This overrides opencode&rsquo;s built-in Anthropic provider rather than declaring a new
        one, so every model the gateway serves is available without listing any of them.
      </>
    ),
    files: [
      {
        label: '~/.config/opencode/opencode.jsonc',
        filename: 'opencode.jsonc',
        lang: 'jsonc',
        body: opencodeJsonc,
        tailor: tailorOpencode,
        // The anthropic provider is the gateway's whole; the default models
        // are its too, since they name its aliases.
        install: {
          path: '.config/opencode/opencode.jsonc',
          merge: { format: 'json', owns: ['provider.anthropic', 'model', 'small_model'] },
        },
      },
    ],
  }),
  new ClientRecipe({
    id: 'crush',
    label: 'crush',
    client: 'crush',
    surface: 'anthropic',
    versioned: false,
    lead: (
      <>
        Every model is spelled out here, and that is deliberate: crush never calls{' '}
        <code>/v1/models</code>, so a model missing from this file cannot be
        selected however well the gateway serves it. The list is the gateway&rsquo;s own, read
        from Anthropic when this page loaded, so download it again when a model is released.
      </>
    ),
    files: [
      {
        label: '~/.config/crush/crush.json',
        filename: 'crush.json',
        lang: 'json',
        body: crushJson,
        tailor: tailorCrush,
        // crush reads %LOCALAPPDATA%\crush on Windows, not ~/.config.
        // Its own provider, and the large and small picks, which name it; the
        // operator's other providers stay.
        install: {
          path: '.config/crush/crush.json',
          windowsPath: 'AppData/Local/crush/crush.json',
          merge: { format: 'json', owns: ['providers.claudication', 'models.large', 'models.small'] },
        },
      },
    ],
  }),
]
