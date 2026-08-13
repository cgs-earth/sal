// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import starlightSiteGraph from "starlight-site-graph";
import { fileURLToPath } from "node:url";
import mermaid from "astro-mermaid";

// https://astro.build/config
export default defineConfig({
  site: "https://cgs-earth.github.io",
  base: "/sal",
  vite: {
    resolve: {
      alias: {
        micromatch: fileURLToPath(
          new URL("./src/shims/micromatch.js", import.meta.url),
        ),
      },
    },
    optimizeDeps: {
      exclude: ["micromatch"],
    },
  },
  integrations: [
    // A single CGS-colored render, on the Cream diagram card cgs.css paints,
    // so diagrams read the same in both the light and the dark theme.
    mermaid({
      theme: "base",
      autoTheme: false,
      mermaidConfig: {
        themeVariables: {
          fontFamily:
            "Aptos, 'Segoe UI Variable Text', 'Segoe UI', system-ui, sans-serif",
          background: "#fefcf5",
          primaryColor: "#c9dee8",
          primaryBorderColor: "#2c84b9",
          primaryTextColor: "#48535b",
          secondaryColor: "#d7eeea",
          secondaryBorderColor: "#46ab9d",
          secondaryTextColor: "#48535b",
          tertiaryColor: "#fdefc0",
          tertiaryBorderColor: "#f9c609",
          tertiaryTextColor: "#48535b",
          lineColor: "#2c84b9",
          textColor: "#48535b",
          mainBkg: "#c9dee8",
          nodeBorder: "#2c84b9",
          nodeTextColor: "#48535b",
          clusterBkg: "#f1f6f9",
          clusterBorder: "#82848b",
          edgeLabelBackground: "#fefcf5",
          titleColor: "#48535b",
          noteBkgColor: "#fdefc0",
          noteBorderColor: "#f9c609",
          noteTextColor: "#48535b",
          actorBkg: "#c9dee8",
          actorBorder: "#2c84b9",
          actorTextColor: "#48535b",
          signalColor: "#48535b",
          signalTextColor: "#48535b",
        },
      },
    }),
    starlight({
      plugins: [starlightSiteGraph()],
      title: "SAL Docs",
      logo: {
        src: "./src/assets/cgs/cgs-mark.png",
        alt: "Center for Geospatial Solutions",
      },
      customCss: ["./src/styles/cgs.css"],
      components: {
        Footer: "./src/components/Footer.astro",
      },
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/cgs-earth/sal",
        },
      ],
      sidebar: [
        {
          label: "Guides",
          items: [{ autogenerate: { directory: "guides" } }],
        },
        {
          label: "Reference",
          items: [{ autogenerate: { directory: "reference" } }],
        },
      ],
    }),
  ],
});
