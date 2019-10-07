import { memoizeAsync } from '../util'
import * as sourcegraph from 'sourcegraph'

export type ExecServerClient = ({
    commands,
    context,
}: Pick<Params, 'commands'> & { context: RepositoryContext }) => Promise<Result>

export interface RepositoryContext {
    repository: string
    commit: string
    path?: string
}

interface Params {
    archiveURL: string
    dir?: string
    commands: string[][]
    includeFiles: string[]
}

export interface Result {
    commands: {
        combinedOutput: string
        ok: boolean
        error?: string
    }[]
    files: { [path: string]: string }
}

export const createExecServerClient = (
    containerName: string,
    includeFiles: Params['includeFiles'],
    cache = true
): ExecServerClient => {
    const baseUrl = new URL(`/.api/extension-containers/${containerName}`, sourcegraph.internal.sourcegraphURL)

    const do2: ExecServerClient = async ({ commands, context }) => {
        const params: Params = {
            archiveURL: getPublicRepoArchiveUrl(context.repository, context.commit),
            commands,
            dir: context.path,
            includeFiles,
        }

        const url = new URL('', baseUrl)
        url.searchParams.set('params', JSON.stringify(params))

        const resp = await fetch(url.toString(), {
            headers: {
                'Content-Type': 'application/json; charset=utf-8',
            },
        })
        if (!resp.ok) {
            throw new Error(`error executing bundler command in ${context.repository}: HTTP ${resp.status}`)
        }
        const result: Result = await resp.json()
        return result
    }
    return cache ? memoizeAsync<Parameters<typeof do2>[0], Result>(do2, arg => JSON.stringify(arg)) : do2
}

function getPublicRepoArchiveUrl(repo: string, commit: string): string {
    const MAP: { [repo: string]: string } = {
        'AC/activeadmin': 'github.com/activeadmin/activeadmin',
        'ACTG/acts-as-taggable-on': 'github.com/mbleigh/acts-as-taggable-on',
        'AD/administrate': 'github.com/thoughtbot/administrate',
        'CAN/cancancan': 'github.com/CanCanCommunity/cancancan',
        'DEV/devise': 'github.com/plataformatec/devise',
        'DIS/discourse': 'github.com/discourse/discourse',
        'FAK/faker': 'github.com/faker-ruby/faker',
        'LIQ/liquid': 'github.com/Shopify/liquid',
        'LOG/logstash': 'github.com/elastic/logstash',
        'OP/openproject': 'github.com/opf/openproject',
        'SID/sidekiq': 'github.com/mperham/sidekiq',
        'SOL/solidus': 'github.com/solidusio/solidus',
        'SPREE/spree': 'github.com/spree/spree',
        'github.com/sd9/openapi-generator': 'github.com/OpenAPITools/openapi-generator',
        'github.com/sd9/redbird': 'github.com/OptimalBits/redbird',
        'github.com/sd9org/react-router': 'github.com/ReactTraining/react-router',
        'github.com/sd9/react-loading-spinner': 'github.com/sourcegraph/react-loading-spinner',
        'github.com/sd9/graphql-dotnet': 'github.com/graphql-dotnet/graphql-dotnet',
        'github.com/sd9/taskbotjs': 'github.com/eropple/taskbotjs',
        'github.com/sd9/ReactStateMuseum': 'github.com/GantMan/ReactStateMuseum',
    }
    if (repo in MAP || sourcegraph.internal.sourcegraphURL.hostname === 'localhost') {
        return `https://sourcegraph.com/${MAP[repo] || repo}@${commit}/-/raw/`
    }
    // TODO!(sqs): requires token
    return new URL(`/${repo}@${commit}/-/raw/`, sourcegraph.internal.sourcegraphURL).toString()
}
