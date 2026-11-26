export const overlayKindLabels = {
  protected_area: 'Protected area',
  civil_district: 'District',
  civil_block: 'Civil block',
  civil_village: 'Village',
  community: 'Community or committee',
  neighbouring_state: 'Neighbouring state'
};

export type OverlayKind = keyof typeof overlayKindLabels;

export interface OverlayMatch {
  kind: OverlayKind;
  layer: string;
  versionId: string;
  featureId: string;
  name: string;
  zone?: 'core' | 'buffer';
}
