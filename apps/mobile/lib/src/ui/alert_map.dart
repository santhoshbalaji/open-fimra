import 'dart:async';
import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
import 'package:maplibre_gl/maplibre_gl.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../auth/oidc.dart';
import '../data/field_store.dart';
import '../map/map_auth.dart';
import '../map/map_style.dart';
import '../theme/app_theme.dart';
import 'home.dart';
import 'widgets.dart';

/// The user's area on a map, with detections from the permission-filtered
/// tiles. Tap a point to open its alert.
class AlertMapView extends StatefulWidget {
  const AlertMapView({super.key, required this.view, required this.onOpen, this.focus});
  final WorkspaceView view;
  final void Function(Alert alert) onOpen;
  final Alert? focus;

  @override
  State<AlertMapView> createState() => _AlertMapViewState();
}

class _AlertMapViewState extends State<AlertMapView> {
  MapLibreMapController? _controller;
  Timer? _tokenTimer;
  late final Future<void> _ready = _setToken();
  String? _problem;
  Brightness? _imageBrightness;

  String get _slug => widget.view.workspace.slug;

  @override
  void initState() {
    super.initState();
    // Access tokens last minutes. Keep the map's copy fresh.
    _tokenTimer = Timer.periodic(const Duration(minutes: 1), (_) => _setToken());
  }

  @override
  void dispose() {
    _tokenTimer?.cancel();
    unawaited(setMapToken(null));
    super.dispose();
  }

  Future<void> _setToken() async {
    try {
      await setMapToken(await widget.view.services.session.accessToken());
    } on SignInException {
      await setMapToken(null);
    }
  }

  Bounds? get _bounds => jurisdictionBounds(widget.view.grants, widget.view.map);

  Future<void> _styleLoaded() async {
    final controller = _controller;
    if (controller == null || !mounted) return;
    final brightness = Theme.of(context).brightness;
    final ratio = MediaQuery.devicePixelRatioOf(context);
    final colour = FimraColours.of(context).neutralMark;
    try {
      if (_imageBrightness != brightness) {
        await controller.addImage(squareImage, await _squarePng(colour, ratio), false, ratio);
        _imageBrightness = brightness;
      }
    } on Exception {
      // The squares are missing, but circles still show.
    }
    if (widget.focus == null) await _fit();
  }

  Future<void> _fit() async {
    final bounds = _bounds;
    if (bounds == null) return;
    await _controller?.moveCamera(
      CameraUpdate.newLatLngBounds(
        LatLngBounds(southwest: LatLng(bounds.south, bounds.west), northeast: LatLng(bounds.north, bounds.east)),
        left: 32,
        top: 32,
        right: 32,
        bottom: 32,
      ),
    );
  }

  Future<void> _tap(Point<double> point, LatLng _) async {
    final controller = _controller;
    if (controller == null) return;
    var features = await controller.queryRenderedFeatures(point, detectionLayers, null);
    if (features.isEmpty) {
      // Keep the symbol small on the map while giving it a finger-sized hit area.
      features = await controller.queryRenderedFeaturesInRect(
        Rect.fromCenter(center: Offset(point.x, point.y), width: FimraSize.touchTarget, height: FimraSize.touchTarget),
        detectionLayers,
        null,
      );
    }
    if (features.isEmpty || !mounted) return;
    // The first feature is the one drawn on top.
    final feature = features.first;
    final props = feature is Map ? feature['properties'] : null;
    final id = props is Map ? props['id'] : null;
    if (id == null) return;
    try {
      final alert = await widget.view.services.repository.alertForDetection(_slug, '$id');
      if (!mounted) return;
      if (alert == null) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('This detection has no alert for you. It may be outside forest.')));
      } else {
        widget.onOpen(alert);
      }
    } on ApiException catch (error) {
      if (mounted) setState(() => _problem = error.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final repo = widget.view.services.offline;
    if (repo == null) return _buildMap(context);
    return ValueListenableBuilder(
      valueListenable: repo.changes,
      builder: (context, _, _) => FutureBuilder(
        future: repo.offline || widget.focus != null
            ? (repo.savedReference(_slug), repo.savedAlerts(_slug)).wait
            : null,
        builder: (context, snap) => _buildMap(
          context,
          reference: snap.data?.$1 ?? (repo.offline || widget.focus != null ? const {} : null),
          alerts: widget.focus == null ? snap.data?.$2 ?? [] : [widget.focus!],
        ),
      ),
    );
  }

  Widget _buildMap(BuildContext context, {Json? reference, List<Alert> alerts = const []}) {
    final p = FimraColours.of(context);
    final style = mapStyle(
      referenceTiles: mapTileUrl(widget.view.services.repository.referenceTiles(_slug)),
      detectionTiles: mapTileUrl(widget.view.services.repository.detectionTiles(_slug)),
      info: widget.view.map,
      palette: p,
      jurisdiction: widget.view.grants,
      offlineReference: reference,
      offlineAlerts: alerts,
    );
    final bounds = _bounds;
    final focus = widget.focus;
    final centre = focus != null
        ? LatLng(focus.latitude, focus.longitude)
        : bounds == null
        ? const LatLng(22, 79)
        : LatLng((bounds.south + bounds.north) / 2, (bounds.west + bounds.east) / 2);
    return FutureBuilder<void>(
      future: reference == null ? _ready : null,
      builder: (context, snapshot) {
        if (reference == null && snapshot.connectionState != ConnectionState.done) {
          return const Center(child: CircularProgressIndicator());
        }
        return Stack(
          children: [
            Semantics(
              container: true,
              sortKey: const OrdinalSortKey(1),
              label: 'Map of your area with fire detections',
              child: MapLibreMap(
                styleString: jsonEncode(style),
                initialCameraPosition: CameraPosition(
                  target: centre,
                  zoom: focus != null
                      ? 13
                      : bounds == null
                      ? 4
                      : 10,
                ),
                onMapCreated: (controller) => _controller = controller,
                onStyleLoadedCallback: () {
                  _imageBrightness = null;
                  unawaited(_styleLoaded());
                },
                onMapClick: focus == null ? _tap : null,
                // Rotation is off, so the map needs no compass.
                compassEnabled: false,
                rotateGesturesEnabled: false,
                tiltGesturesEnabled: false,
                myLocationEnabled: false,
                attributionEnabled: false,
              ),
            ),
            Positioned(right: 12, top: 12, child: AlertMapControls(onFit: _fit)),
            if (_problem != null)
              Positioned(
                left: 12,
                right: 12,
                bottom: 12,
                child: Card(
                  child: ListTile(
                    leading: Icon(Icons.error_outline, color: p.danger),
                    title: Text(_problem!),
                    trailing: IconButton(
                      tooltip: 'Dismiss',
                      icon: const Icon(Icons.close),
                      onPressed: () => setState(() => _problem = null),
                    ),
                  ),
                ),
              ),
          ],
        );
      },
    );
  }
}

class AlertMapControls extends StatelessWidget {
  const AlertMapControls({super.key, required this.onFit});
  final VoidCallback onFit;

  @override
  Widget build(BuildContext context) => Semantics(
    container: true,
    explicitChildNodes: true,
    sortKey: const OrdinalSortKey(0),
    child: Column(
      children: [
        _MapButton(icon: Icons.center_focus_strong, tooltip: 'Show my area', onPressed: onFit),
        const SizedBox(height: 8),
        _MapButton(
          icon: Icons.info_outline,
          tooltip: 'Map key',
          onPressed: () => showModalBottomSheet<void>(
            context: context,
            useSafeArea: true,
            scrollControlDisabledMaxHeightRatio: 0.48,
            builder: (context) => const SafeArea(top: false, child: MapKey()),
          ),
        ),
      ],
    ),
  );
}

class _MapButton extends StatelessWidget {
  const _MapButton({required this.icon, required this.tooltip, required this.onPressed});
  final IconData icon;
  final String tooltip;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) => Material(
    color: FimraColours.of(context).card,
    elevation: 2,
    shape: CircleBorder(side: BorderSide(color: FimraColours.of(context).controlBoundary)),
    child: MergeSemantics(
      child: Tooltip(
        message: tooltip,
        excludeFromSemantics: true,
        child: Semantics(
          label: tooltip,
          child: IconButton(icon: Icon(icon), onPressed: onPressed),
        ),
      ),
    ),
  );
}

/// Explains the map symbols: shape for forest class, colour for age.
class MapKey extends StatelessWidget {
  const MapKey({super.key});

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final shapes = _KeyGroup(
      title: 'Shape shows the forest class',
      entries: [
        for (final forest in ForestClass.values) (DetectionSymbol(forestClass: forest, size: 20), forest.label),
      ],
    );
    final ages = _KeyGroup(
      title: 'Colour shows time since detection',
      entries: [
        for (var age = 0; age < fireAgeSteps.length; age++)
          (
            DetectionSymbol(forestClass: ForestClass.inside, age: age, size: 20),
            fireAgeSteps[age][0].toUpperCase() + fireAgeSteps[age].substring(1),
          ),
      ],
    );
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Expanded(
                child: Semantics(headingLevel: 1, child: Text('Map key', style: text.titleLarge)),
              ),
              OutlinedButton(onPressed: () => Navigator.of(context).pop(), child: const Text('Close')),
            ],
          ),
          Flexible(
            child: SingleChildScrollView(
              padding: const EdgeInsets.only(top: 8, bottom: 8),
              child: LayoutBuilder(
                builder: (context, constraints) {
                  final sideBySide = constraints.maxWidth >= 315 * MediaQuery.textScalerOf(context).scale(16) / 16;
                  return Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      if (sideBySide)
                        Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Expanded(child: shapes),
                            const SizedBox(width: 16),
                            Expanded(child: ages),
                          ],
                        )
                      else ...[
                        shapes,
                        const SizedBox(height: 12),
                        ages,
                      ],
                      const SizedBox(height: 8),
                      const Text('Your own area has a thick outline.'),
                    ],
                  );
                },
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _KeyGroup extends StatelessWidget {
  const _KeyGroup({required this.title, required this.entries});
  final String title;
  final List<(DetectionSymbol, String)> entries;

  @override
  Widget build(BuildContext context) => Semantics(
    container: true,
    explicitChildNodes: true,
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Semantics(headingLevel: 2, child: Text(title, style: Theme.of(context).textTheme.titleSmall)),
        const SizedBox(height: 4),
        for (final (symbol, label) in entries)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 4),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                symbol,
                const SizedBox(width: 8),
                Expanded(child: Text(label)),
              ],
            ),
          ),
      ],
    ),
  );
}

Future<Uint8List> _squarePng(Color colour, double ratio) async {
  final size = (12 * ratio).ceil();
  final recorder = ui.PictureRecorder();
  final canvas = Canvas(recorder);
  final inset = size * 0.1;
  canvas.drawRect(Rect.fromLTWH(inset, inset, size - 2 * inset, size - 2 * inset), Paint()..color = colour);
  final image = await recorder.endRecording().toImage(size, size);
  final data = await image.toByteData(format: ui.ImageByteFormat.png);
  return data!.buffer.asUint8List();
}
