/** @type {import('next').NextConfig} */
const nextConfig = {
  // The site is pure static content: `next build` writes a deployable `out/`
  // directory that any file server (or GitHub Pages) can host as-is.
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
};

export default nextConfig;
