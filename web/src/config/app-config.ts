/**
 * app-config.ts: application-level static config (name, version, copyright, meta info).
 * Main functions/types:
 *   - APP_CONFIG: aggregated application config constants
 */
import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "ARTEX Defense",
  version: packageJson.version,
  copyright: `© ${currentYear}, ARTEX Defense.`,
  meta: {
    title: "ARTEX Defense — Evidence-backed Remediation",
    description: "Local security finding remediation with independently validated patches",
  },
};
