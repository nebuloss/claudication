import { type ReactNode } from 'react'
import { type DialectId, type InstallFile, type InstallMerge } from '../../api/client'
import { type Lang } from '../primitives/code'
import { type Setup } from './configs'

/**
 * The example address inside configs/clients/*.
 *
 * Those are plain, complete, committed files — readable in the repository,
 * linked from the README, and usable as they stand after one edit. Swapping
 * this for a gateway's own address is a string replace rather than a template
 * language with a renderer at each end.
 */
export const EXAMPLE_BASE = 'https://claudication.example.com'

/**
 * The example key inside the same files, replaced by a client's own key on
 * its page while the key is still offered.
 */
export const EXAMPLE_KEY = 'clc_...'

/** One file a client is configured with. */
export type ConfigFile = {
  /** Where it goes, as a reader would write the path. */
  label: string
  /** What it is called when downloaded. */
  filename: string
  lang: Lang
  /** The committed file, imported as text. */
  body: string
  /**
   * Writes the file for one client: its names in every model field, its API
   * where its type has two. `base` is the example address, which the caller
   * swaps for the gateway's afterwards. See configs.ts.
   */
  write: (body: string, setup: Setup, base: string) => string
  /** Where the one-line installer puts it. */
  install: InstallTarget
}

/**
 * Where a file goes on a client's machine, relative to the home directory, as
 * the installer (internal/service/install) writes it.
 */
export type InstallTarget = {
  path: string
  /** Where it goes on Windows, when that is somewhere else. */
  windowsPath?: string
  /**
   * A file of shell exports meant to be in every shell: sourced from the
   * profile on Linux and macOS, set as user variables on Windows.
   */
  env?: boolean
  /**
   * Set on a client's own config, which holds more than the gateway's
   * settings: the installer then rewrites only what the gateway owns and
   * keeps the rest. Absent on a file that is the gateway's alone.
   */
  merge?: InstallMerge
}

/** What a client type's files need, before any of it is resolved. */
export type RecipeData = {
  /** The client type's id (internal/service/clients). */
  id: string
  /**
   * Whether the base URL this client wants carries /v1, per API.
   *
   * The single most common way this goes wrong, and the clients genuinely
   * disagree: Claude Code and crush on Messages append it themselves,
   * opencode and Codex do not, and crush on Responses wants it given.
   */
  versioned: Partial<Record<DialectId, boolean>>
  lead: ReactNode
  files: ConfigFile[]
  notes?: ReactNode[]
}

/**
 * One client type's recipe: its files and how each is written for a client.
 *
 * A class rather than a bag of fields and helpers beside it, because every
 * question a page asks — what address to give it, which files with which
 * contents, what the installer is handed — is a question about *that* type
 * with an answer that depends on its own data. The view asks; a new type is
 * a new entry rather than a new branch.
 */
export class ClientRecipe {
  constructor(private readonly data: RecipeData) {}

  get id(): string {
    return this.data.id
  }

  get lead(): ReactNode {
    return this.data.lead
  }

  get notes(): ReactNode[] {
    return this.data.notes ?? []
  }

  /** The address to give this client on an API, which may or may not carry /v1. */
  baseURL(gateway: string, dialect: DialectId): string {
    return this.data.versioned[dialect] === true ? `${gateway}/v1` : gateway
  }

  /**
   * Its files: as committed (no setup), or written for one
   * client from its setup; then the example address replaced by the
   * gateway's, and the example key by the client's own when there is one to
   * give.
   */
  files(gateway: string, key = '', setup?: Setup): ConfigFile[] {
    return this.data.files.map((f) => {
      let body = f.body
      if (setup !== undefined) {
        try {
          body = f.write(body, setup, EXAMPLE_BASE)
        } catch {
          // A committed file the rewrite cannot read is still a working file
          // for the type's default names: hand it out rather than nothing.
          body = f.body
        }
      }
      body = body.split(EXAMPLE_BASE).join(gateway)
      if (key !== '') body = body.split(EXAMPLE_KEY).join(key)
      return { ...f, body }
    })
  }

  /** The files as the one-line installer takes them. */
  installFiles(label: string, gateway: string, key: string, setup: Setup): InstallFile[] {
    return this.files(gateway, key, setup).map((f) => ({
      client_id: this.data.id,
      client: label,
      path: f.install.path,
      windows_path: f.install.windowsPath,
      env: f.install.env,
      merge: f.install.merge,
      body: f.body,
    }))
  }
}
