import { Component, ElementRef, effect, inject, input, viewChild } from '@angular/core';
import { BarChart } from 'echarts/charts';
import { GridComponent } from 'echarts/components';
import { init, use } from 'echarts/core';
import { SVGRenderer } from 'echarts/renderers';
import { classes, ForestClass } from './detections';
import { ThemeService } from './theme';

use([BarChart, GridComponent, SVGRenderer]);

/** Reads the theme's CSS variables, so the chart follows the same tokens as the rest of the page. */
export function chartColors(root: Element = document.documentElement) {
  const style = getComputedStyle(root);
  const read = (name: string) => style.getPropertyValue('--color-' + name).trim();
  return {
    text: read('text'), textSecondary: read('text-secondary'), border: read('border'),
    font: style.getPropertyValue('--font-sans').trim(),
    // Forest classes keep a fixed chart colour order: the first class always uses chart colour 1.
    series: Object.keys(classes).map((_, index) => read('chart-' + (index + 1)))
  };
}

@Component({selector: 'fimra-class-chart', host: {class: 'block'}, template: '<div #plot class="h-44 w-full" aria-hidden="true"></div>'})
export class ClassChart {
  readonly counts = input.required<Partial<Record<ForestClass, number>>>();
  private readonly plot = viewChild<ElementRef<HTMLDivElement>>('plot');
  private readonly appearance = inject(ThemeService);

  constructor() {
    effect(onCleanup => {
      const el = this.plot()?.nativeElement;
      if (!el) return;
      // The theme service writes data-theme first, so the variables below match this theme.
      this.appearance.theme();
      const data = this.counts();
      const keys = Object.keys(classes) as ForestClass[];
      const chart = init(el, undefined, {renderer: 'svg'});
      const color = chartColors();
      chart.setOption({
        animation: false,
        textStyle: {fontFamily: color.font},
        grid: {left: 8, right: 40, top: 8, bottom: 24, containLabel: true},
        yAxis: {type: 'category', inverse: true, data: keys.map(k => classes[k]),
          axisLabel: {color: color.text, fontSize: 12}, axisTick: {show: false},
          axisLine: {lineStyle: {color: color.border}}},
        xAxis: {type: 'value', minInterval: 1, axisLabel: {color: color.textSecondary},
          splitLine: {lineStyle: {color: color.border}}},
        series: [{type: 'bar', barMaxWidth: 24, label: {show: true, position: 'right', color: color.text},
          data: keys.map((key, index) => ({value: data[key] ?? 0, itemStyle: {color: color.series[index]}}))}]
      });
      const observer = new ResizeObserver(() => chart.resize());
      observer.observe(el);
      onCleanup(() => { observer.disconnect(); chart.dispose(); });
    });
  }
}
