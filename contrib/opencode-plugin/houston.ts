/**
 * Houston Discovery Plugin for OpenCode
 * 
 * Enables houston to discover and embed OpenCode instances.
 * 
 * Features:
 * - Writes server info for discovery
 * - Adds houston origin to CORS allowlist
 * 
 * Discovery file: ~/.local/state/houston/opencode-servers/{pid}.json
 */

import type { Plugin } from "@opencode-ai/plugin"
import * as fs from "fs"
import * as path from "path"
import * as os from "os"

const DISCOVERY_DIR = path.join(os.homedir(), ".local", "state", "houston", "opencode-servers")

// Default houston URLs to allow for CORS/embedding
const HOUSTON_ORIGINS = [
  "http://127.0.0.1:9090",
  "http://localhost:9090",
  "http://127.0.0.1:9091",
  "http://localhost:9091",
  // Add more if needed
]

interface ServerInfo {
  pid: number
  url: string
  project: string
  directory: string
  startedAt: string
  houstonOrigins: string[]
}

function ensureDir(dir: string) {
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true })
  }
}

function writeServerInfo(info: ServerInfo) {
  ensureDir(DISCOVERY_DIR)
  const filePath = path.join(DISCOVERY_DIR, `${info.pid}.json`)
  fs.writeFileSync(filePath, JSON.stringify(info, null, 2))
}

function removeServerInfo(pid: number) {
  const filePath = path.join(DISCOVERY_DIR, `${pid}.json`)
  try {
    fs.unlinkSync(filePath)
  } catch {
    // Ignore
  }
}

function cleanupStale() {
  ensureDir(DISCOVERY_DIR)
  try {
    const files = fs.readdirSync(DISCOVERY_DIR)
    
    for (const file of files) {
      if (!file.endsWith(".json")) continue
      
      const pid = parseInt(file.replace(".json", ""), 10)
      if (isNaN(pid)) continue
      
      try {
        process.kill(pid, 0)
      } catch {
        const filePath = path.join(DISCOVERY_DIR, file)
        try {
          fs.unlinkSync(filePath)
        } catch {
          // Ignore
        }
      }
    }
  } catch {
    // Directory might not exist
  }
}

export const HoustonPlugin: Plugin = async ({ project, directory, serverUrl }) => {
  const pid = process.pid
  
  cleanupStale()
  
  // Register cleanup
  const cleanup = () => removeServerInfo(pid)
  process.on("exit", cleanup)
  process.on("SIGINT", () => { cleanup(); process.exit(0) })
  process.on("SIGTERM", () => { cleanup(); process.exit(0) })

  // Write discovery info
  const info: ServerInfo = {
    pid,
    url: serverUrl.toString(),
    project: project?.name || path.basename(directory),
    directory,
    startedAt: new Date().toISOString(),
    houstonOrigins: HOUSTON_ORIGINS,
  }
  
  writeServerInfo(info)
  
  return {}
}
