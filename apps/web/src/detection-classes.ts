export const classes = {
  inside: 'Inside forest', near_edge: 'Near the forest edge',
  outside: 'Outside forest', fixed_heat_source: 'Suspected fixed heat source'
};
export type ForestClass = keyof typeof classes;
