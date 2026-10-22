import { type ReactNode } from 'react'
import { type ClientFile } from '../../api/client'

/**
 * The example address inside configs/clients/*, which a page shows when it
 * cannot tell the relay's own (relayAddress).
 */
export const EXAMPLE_BASE = 'https://claudication.example.com'

/**
 * The example key inside the same files, replaced by a client's own key on
 * its page while the key is still offered.
 */
export const EXAMPLE_KEY = 'clc_...'

/** What a client type's page says around its files. */
export type RecipeData = {
  /** The client type's id (internal/service/clients). */
  id: string
  /**
   * Whether the base URL this client wants carries /v1.
   *
   * The single most common way this goes wrong, and the clients genuinely
   * disagree: Claude Code and crush append it themselves, opencode and Codex
   * do not.
   */
  versioned: boolean
  lead: ReactNode
  notes?: ReactNode[]
}

/**
 * One client type's recipe: the words around its files and the address it
 * wants. The files themselves are the gateway's to write
 * (internal/service/clientconfig, GET /admin/clients/{id}/files), from the
 * committed ones under configs/clients — one writer, which the install and
 * update commands and the MCP server's update_config all use, so what the
 * page shows is what they install.
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

  /** The address to give this client, which may or may not carry /v1. */
  baseURL(gateway: string): string {
    return this.data.versioned ? `${gateway}/v1` : gateway
  }
}

/** A file with the client's key put where the example's is, while it is offered. */
export function withKey(f: ClientFile, key: string): ClientFile {
  return key === '' ? f : { ...f, body: f.body.split(EXAMPLE_KEY).join(key) }
}
