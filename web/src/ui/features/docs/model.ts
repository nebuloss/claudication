import { type ReactNode } from 'react'
import { type Surface } from '../../../api/client'
import { type Lang } from '../../primitives/code'

/**
 * The example address inside configs/clients/*.
 *
 * Those are plain, complete, committed files — readable in the repository,
 * linked from the README, and usable as they stand after one edit. Swapping
 * this for a gateway's own address is the only thing done to them, and it is a
 * string replace rather than a template language with a renderer at each end.
 */
export const EXAMPLE_BASE = 'https://claudication.example.com'

/**
 * The example key inside the same files, replaced the same way when a real one
 * is known — which is only ever in the dialog that shows a key the moment it
 * is minted. The public page never has one and leaves this as it is.
 */
export const EXAMPLE_KEY = 'clc_...'

/** Which client-facing API a client speaks. */
export type SurfaceID = 'anthropic' | 'openai' | 'both'

/** One file a client is configured with. */
export type ConfigFile = {
  /** Where it goes, as a reader would write the path. */
  label: string
  /** What it is called when downloaded. */
  filename: string
  lang: Lang
  /** The committed file, imported as text. */
  body: string
}

/** What a client needs, before any of it is resolved against a gateway. */
export type ClientData = {
  id: string
  label: string
  surface: SurfaceID
  /**
   * Whether the base URL this client wants carries /v1.
   *
   * The single most common way this goes wrong, and the clients genuinely
   * disagree: Claude Code and crush append it themselves, opencode and Codex
   * do not. Carried as data rather than written into each description, so the
   * page can state it per client without anyone having to remember to.
   */
  versioned: boolean
  lead: ReactNode
  files: ConfigFile[]
  notes?: ReactNode[]
}

/**
 * One client's recipe, resolved against a particular gateway.
 *
 * A class rather than a bag of fields and a handful of helpers beside it,
 * because every question the page asks about a client — what address to give
 * it, which files with which contents, whether the API it needs is switched
 * off — is a question about *that* client and has an answer that depends on
 * its own data. Keeping those answers here means the view asks rather than
 * computes, and a new client is a new entry rather than a new branch.
 */
export class ClientRecipe {
  constructor(private readonly data: ClientData) {}

  get id(): string {
    return this.data.id
  }

  get label(): string {
    return this.data.label
  }

  get lead(): ReactNode {
    return this.data.lead
  }

  get notes(): ReactNode[] {
    return this.data.notes ?? []
  }

  /** The address to give this client, which may or may not carry /v1. */
  baseURL(gateway: string): string {
    return this.data.versioned ? `${gateway}/v1` : gateway
  }

  /**
   * Its files, with the example address replaced by this gateway's own, and
   * the example key by a real one when there is one to give.
   */
  files(gateway: string, key = ''): ConfigFile[] {
    return this.data.files.map((f) => {
      let body = f.body.split(EXAMPLE_BASE).join(gateway)
      if (key !== '') body = body.split(EXAMPLE_KEY).join(key)
      return { ...f, body }
    })
  }

  /**
   * The APIs this client needs that are currently switched off.
   *
   * Empty is the normal answer and the page says nothing; anything else is a
   * configuration that will answer 404 however carefully it is copied, which
   * is worth saying next to the file rather than leaving to be discovered.
   */
  missingSurfaces(surfaces: Surface[]): Surface[] {
    return surfaces.filter(
      (s) => !s.enabled && (this.data.surface === 'both' || s.id === this.data.surface),
    )
  }
}

/** A symptom and what actually causes it. */
export type Symptom = { symptom: string; cause: ReactNode }

/** One addressable part of the page, for the heading and the contents list. */
export type Section = { id: string; title: string }
