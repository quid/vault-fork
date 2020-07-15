import fetch from 'isomorphic-unfetch'
import { VERSION, CHANGELOG_URL } from '../../data/version.js'
import ProductDownloader from '@hashicorp/react-product-downloader'
import Head from 'next/head'
import HashiHead from '@hashicorp/react-head'

export default function DownloadsPage({ releaseData }) {
  const changelogUrl = CHANGELOG_URL.length
    ? CHANGELOG_URL
    : `https://github.com/quid/vault/blob/v${VERSION}/CHANGELOG.md`
  return (
    <div id="p-downloads" className="g-container">
      <HashiHead is={Head} title="Downloads | Vault by Hashicorp" />
      <ProductDownloader
        product="Vault"
        version={VERSION}
        releaseData={releaseData}
        changelog={changelogUrl}
        prerelease={{
          type: 'release candidate',
          name: 'v1.5.0',
          version: '1.5.0-rc',
        }}
      />
    </div>
  )
}

export async function getStaticProps() {
  return fetch(`https://releases.hashicorp.com/vault/${VERSION}/index.json`)
    .then((r) => r.json())
    .then((releaseData) => ({ props: { releaseData } }))
    .catch(() => {
      throw new Error(
        `--------------------------------------------------------
        Unable to resolve version ${VERSION} on releases.hashicorp.com from link
        <https://releases.hashicorp.com/vault/${VERSION}/index.json>. Usually this
        means that the specified version has not yet been released. The downloads page
        version can only be updated after the new version has been released, to ensure
        that it works for all users.
        ----------------------------------------------------------`
      )
    })
}
