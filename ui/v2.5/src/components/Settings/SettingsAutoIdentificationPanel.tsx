import React, { useEffect, useMemo, useState } from "react";
import { Alert, Button, Collapse, Form, ListGroup } from "react-bootstrap";
import { faCog } from "@fortawesome/free-solid-svg-icons";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import { useListSceneScrapers } from "src/core/StashService";
import { Icon } from "src/components/Shared/Icon";
import { IScraperSource } from "src/components/Dialogs/IdentifyDialog/constants";
import { OptionsEditor } from "src/components/Dialogs/IdentifyDialog/Options";
import { BooleanSetting, Setting } from "./Inputs";
import { SettingSection } from "./SettingSection";
import { useSettings } from "./context";

interface SourceChoice {
  id: string;
  name: string;
  source: GQL.ScraperSourceInput;
}

function sourceKey(source: GQL.ScraperSourceInput) {
  if (source.scraper_id) return `scraper:${source.scraper_id}`;
  if (source.stash_box_endpoint) return `stashbox:${source.stash_box_endpoint}`;
  return "";
}

function sourceOptionsInput(
  options?:
    | GQL.Maybe<GQL.IdentifyMetadataOptions>
    | GQL.InputMaybe<GQL.IdentifyMetadataOptionsInput>
): GQL.IdentifyMetadataOptionsInput | undefined {
  if (!options) return undefined;
  return {
    fieldOptions: options.fieldOptions?.map((field) => ({
      field: field.field,
      strategy: field.strategy,
      createMissing: field.createMissing ?? undefined,
    })),
    setCoverImage: options.setCoverImage ?? undefined,
    setOrganized: options.setOrganized ?? undefined,
    includeMalePerformers: options.includeMalePerformers ?? undefined,
    performerGenders: options.performerGenders ?? undefined,
    skipMultipleMatches: options.skipMultipleMatches ?? undefined,
    skipMultipleMatchTag: options.skipMultipleMatchTag ?? undefined,
    skipSingleNamePerformers: options.skipSingleNamePerformers ?? undefined,
    skipSingleNamePerformerTag: options.skipSingleNamePerformerTag ?? undefined,
  };
}

function getDefaultAutoIdentifyOptions(): GQL.IdentifyMetadataOptionsInput {
  const fields = [
    "title",
    "code",
    "date",
    "production_date",
    "director",
    "details",
    "cover_image",
    "url",
    "studio",
    "performers",
    "tags",
    "stash_ids",
  ];
  return {
    fieldOptions: fields.map((field) => ({
      field,
      strategy: GQL.IdentifyFieldStrategy.Merge,
      createMissing: ["studio", "performers", "tags"].includes(field),
    })),
    setCoverImage: true,
    skipMultipleMatches: true,
    skipSingleNamePerformers: true,
  };
}

export const SettingsAutoIdentificationPanel: React.FC = () => {
  const intl = useIntl();
  const { general, defaults, ui, saveGeneral } = useSettings();
  const { data: scraperData, error: scraperError } = useListSceneScrapers();
  const libraries = general.stashes ?? [];
  const rules = general.autoIdentifyLibraries ?? [];
  const [selectedPath, setSelectedPath] = useState(libraries[0]?.path ?? "");
  const [newSource, setNewSource] = useState("");
  const [editingSourceIndex, setEditingSourceIndex] = useState<number | null>(
    null
  );
  const [editingField, setEditingField] = useState(false);

  useEffect(() => {
    if (!libraries.some((library) => library.path === selectedPath)) {
      setSelectedPath(libraries[0]?.path ?? "");
    }
  }, [libraries, selectedPath]);

  const availableSources = useMemo<SourceChoice[]>(() => {
    const stashBoxes = (general.stashBoxes ?? []).map((stashBox) => ({
      id: `stashbox:${stashBox.endpoint}`,
      name: `Stash-Box: ${stashBox.name}`,
      source: { stash_box_endpoint: stashBox.endpoint },
    }));
    const scrapers = (scraperData?.listScrapers ?? [])
      .filter((scraper) =>
        scraper.scene?.supported_scrapes.includes(GQL.ScrapeType.Fragment)
      )
      .map((scraper) => ({
        id: `scraper:${scraper.id}`,
        name: scraper.name,
        source: { scraper_id: scraper.id },
      }));
    return [...stashBoxes, ...scrapers];
  }, [general.stashBoxes, scraperData]);

  const rule = rules.find((item) => item.path === selectedPath);
  const sources: GQL.IdentifySourceInput[] = (rule?.sources ?? []).map(
    (item) => ({
      source: {
        scraper_id: item.source.scraper_id ?? undefined,
        stash_box_endpoint: item.source.stash_box_endpoint ?? undefined,
      },
      options: sourceOptionsInput(item.options ?? undefined),
    })
  );
  const selectedSourceIDs = sources.map((item) => sourceKey(item.source));
  const remainingSources = availableSources.filter(
    (item) => !selectedSourceIDs.includes(item.id)
  );

  function saveLibrary(updated: Partial<GQL.AutoIdentifyLibraryConfigInput>) {
    if (!selectedPath) return;
    const nextRule: GQL.AutoIdentifyLibraryConfigInput = {
      path: selectedPath,
      autoScan: rule?.autoScan ?? false,
      autoIdentify: rule?.autoIdentify ?? false,
      ignoreOrganizedScenes: rule?.ignoreOrganizedScenes ?? false,
      sources,
      ...updated,
    };
    const nextRules = rules.filter((item) => item.path !== selectedPath);
    nextRules.push(nextRule);
    saveGeneral({ autoIdentifyLibraries: nextRules });
  }

  function addSource() {
    const choice = remainingSources.find((item) => item.id === newSource);
    if (!choice) return;
    saveLibrary({ sources: [...sources, { source: choice.source }] });
    setNewSource("");
  }

  function moveSource(index: number, direction: -1 | 1) {
    const next = [...sources];
    const target = index + direction;
    [next[index], next[target]] = [next[target], next[index]];
    saveLibrary({ sources: next });
    setEditingSourceIndex(null);
  }

  function updateSourceOptions(
    index: number,
    options: GQL.IdentifyMetadataOptionsInput
  ) {
    saveLibrary({
      sources: sources.map((source, sourceIndex) =>
        sourceIndex === index ? { ...source, options } : source
      ),
    });
  }

  const scanDefaults =
    (ui.taskDefaults?.scan as GQL.ScanMetadataInput | undefined) ??
    defaults.scan;
  const phashesEnabled = scanDefaults?.scanGeneratePhashes ?? false;

  if (scraperError) return <h1>{scraperError.message}</h1>;

  return (
    <SettingSection
      headingID="config.library.auto_scan_identify"
      subHeadingID="config.library.auto_identification_description"
    >
      <Setting
        headingID="config.library.auto_identify_library"
        subHeadingID="config.library.auto_identify_library_description"
      >
        <Form.Control
          as="select"
          className="input-control"
          value={selectedPath}
          disabled={libraries.length === 0}
          onChange={(event) => {
            setSelectedPath(event.currentTarget.value);
            setEditingSourceIndex(null);
          }}
        >
          {libraries.map((library) => (
            <option key={library.path} value={library.path}>
              {library.path}
            </option>
          ))}
        </Form.Control>
      </Setting>

      {selectedPath && (
        <>
          <BooleanSetting
            id="library-auto-scan"
            headingID="config.library.auto_scan"
            subHeadingID="config.library.auto_scan_description"
            checked={rule?.autoScan ?? false}
            onChange={(v) => saveLibrary({ autoScan: v })}
          />
          <BooleanSetting
            id="library-auto-identify"
            headingID="config.library.auto_identify"
            subHeadingID="config.library.auto_identify_description"
            checked={rule?.autoIdentify ?? false}
            onChange={(v) => saveLibrary({ autoIdentify: v })}
          />
          {rule?.autoIdentify && (
            <BooleanSetting
              id="library-ignore-organized-scenes"
              headingID="config.library.ignore_organized_scenes"
              subHeadingID="config.library.ignore_organized_scenes_description"
              checked={rule.ignoreOrganizedScenes ?? false}
              onChange={(v) => saveLibrary({ ignoreOrganizedScenes: v })}
            />
          )}

          {rule?.autoIdentify && !phashesEnabled && (
            <Alert variant="warning">
              <FormattedMessage id="config.library.auto_identify_phash_warning" />
            </Alert>
          )}

          <div className="setting">
            <div>
              <h3>
                {intl.formatMessage({
                  id: "config.library.auto_identify_sources",
                })}
              </h3>
              <div className="sub-heading">
                <FormattedMessage
                  id="config.library.auto_identify_source_library"
                  values={{ path: selectedPath }}
                />
                <br />
                {intl.formatMessage({
                  id: "config.library.auto_identify_sources_description",
                })}
              </div>
            </div>
            <div>
              <ListGroup as="ol">
                {sources.map((item, index) => {
                  const choice = availableSources.find(
                    (source) => source.id === sourceKey(item.source)
                  );
                  const sourceForOptions: IScraperSource = {
                    id: sourceKey(item.source),
                    displayName: choice?.name ?? sourceKey(item.source),
                    scraper_id: item.source.scraper_id ?? undefined,
                    stash_box_endpoint:
                      item.source.stash_box_endpoint ?? undefined,
                    options: item.options ?? undefined,
                  };
                  return (
                    <ListGroup.Item
                      as="li"
                      key={sourceKey(item.source)}
                      className="bg-dark text-light border-secondary"
                    >
                      <div className="d-flex justify-content-between align-items-center">
                        <span>
                          <FormattedMessage id="config.library.auto_identify_selected_source" />
                          {": "}
                          <strong>
                            {choice?.name ?? sourceKey(item.source)}
                          </strong>
                        </span>
                        <span>
                          <Button
                            className="minimal"
                            aria-label={intl.formatMessage({
                              id: "config.library.edit_source_options",
                            })}
                            aria-expanded={editingSourceIndex === index}
                            onClick={() =>
                              setEditingSourceIndex(
                                editingSourceIndex === index ? null : index
                              )
                            }
                          >
                            <Icon icon={faCog} />
                          </Button>
                          <Button
                            className="minimal"
                            disabled={index === 0}
                            aria-label={intl.formatMessage({
                              id: "config.library.move_source_up",
                            })}
                            onClick={() => moveSource(index, -1)}
                          >
                            ↑
                          </Button>
                          <Button
                            className="minimal"
                            disabled={index === sources.length - 1}
                            aria-label={intl.formatMessage({
                              id: "config.library.move_source_down",
                            })}
                            onClick={() => moveSource(index, 1)}
                          >
                            ↓
                          </Button>
                          <Button
                            className="minimal text-danger"
                            onClick={() => {
                              saveLibrary({
                                sources: sources.filter((_, i) => i !== index),
                              });
                              setEditingSourceIndex(null);
                            }}
                          >
                            <FormattedMessage id="actions.remove" />
                          </Button>
                        </span>
                      </div>
                      <Collapse in={editingSourceIndex === index}>
                        <div className="mt-3">
                          <OptionsEditor
                            options={item.options ?? {}}
                            setOptions={(options) =>
                              updateSourceOptions(index, options)
                            }
                            source={sourceForOptions}
                            setEditingField={(value) => setEditingField(value)}
                            defaultOptions={getDefaultAutoIdentifyOptions()}
                          />
                        </div>
                      </Collapse>
                    </ListGroup.Item>
                  );
                })}
              </ListGroup>
              <div className="d-flex mt-2">
                <Form.Control
                  as="select"
                  className="input-control mr-2"
                  value={newSource}
                  onChange={(event) => setNewSource(event.currentTarget.value)}
                >
                  <option value="">
                    {intl.formatMessage({
                      id: "config.library.select_auto_identify_source",
                    })}
                  </option>
                  {remainingSources.map((source) => (
                    <option key={source.id} value={source.id}>
                      {source.name}
                    </option>
                  ))}
                </Form.Control>
                <Button
                  variant="secondary"
                  disabled={!newSource || editingField}
                  onClick={addSource}
                >
                  <FormattedMessage id="actions.add" />
                </Button>
              </div>
            </div>
          </div>
        </>
      )}
    </SettingSection>
  );
};
