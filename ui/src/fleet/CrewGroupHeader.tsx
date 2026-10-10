import { countsLabel, dispatchHref } from './crewsModel'
import type { FleetEntry } from './fleetEntries'
import { crewShortId } from './fleetList'
import { resolveCrewRepo, useCrewRepos } from './useCrewRepos'

type CrewEntry = Extract<FleetEntry, { kind: 'crew' }>

export function CrewGroupHeader({ entry }: { entry: CrewEntry }) {
  const repo = resolveCrewRepo(entry.crew, entry.project, useCrewRepos(true))
  return (
    <div className="fleet-group-head">
      <span className="fleet-group-repo">{repo?.name ?? entry.project ?? 'unknown repo'}</span>
      <span className="fleet-group-id" title={entry.crew}>
        {crewShortId(entry.crew)}
      </span>
      <span className="fleet-group-counts">{countsLabel(entry.counts)}</span>
      {repo && (
        <a className="fleet-group-dispatch" href={dispatchHref(repo.path, entry.crew)}>
          Dispatch here
        </a>
      )}
    </div>
  )
}
