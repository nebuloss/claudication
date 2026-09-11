/// <reference types="vite/client" />

// The example client configs under configs/clients are imported as text, so
// there is one copy of each rather than a second set pasted into a component.
// Vite resolves the `?raw` suffix; TypeScript needs telling what comes back.
declare module '*?raw' {
  const contents: string
  export default contents
}
