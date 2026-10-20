import { api } from '../../../api/client'
import { useLoader } from '../../hooks'
import { ClientTypes } from '../../clients/specs'

/**
 * The client types, loaded once per screen that needs them: every type's
 * slots and labels, the APIs and the targets a slot can follow. The server's
 * table (internal/service/clients), so a label here cannot drift from it.
 */
export function useClientTypes() {
  const { data, error, loading, reload } = useLoader((signal) => api.clients.types(signal))
  return { types: data === null ? null : new ClientTypes(data), error, loading, reload }
}
