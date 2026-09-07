import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './ui/app'
import './ui/styles/index.css'

const root = document.getElementById('root')
if (root === null) {
  throw new Error('claudication: #root is missing from index.html')
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
