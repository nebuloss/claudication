import { ClientRecipe } from './model'

/**
 * Every client type the gateway hands configs to, as data: its address rule
 * and what its page says around its files.
 *
 * The files are not here. They are the committed ones under configs/clients,
 * written for each client by the gateway (internal/service/clientconfig), so
 * there is one copy of each and one writer. The types' slots and labels are
 * not here either: they are the server's (/admin/client-types).
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
  }),
]

/** A type's recipe, or none for a custom client. */
export function recipeFor(typeId: string): ClientRecipe | undefined {
  return RECIPES.find((r) => r.id === typeId)
}
