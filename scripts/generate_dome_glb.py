#!/usr/bin/env python3
"""Generate the deterministic icosphere used by the ambient dashboard scene."""

from __future__ import annotations

import json
import math
import struct
import sys
from pathlib import Path


LEVEL = 5
EXPECTED_VERTICES = 10 * (4**LEVEL) + 2
EXPECTED_TRIANGLES = 20 * (4**LEVEL)


def normalized(vector):
    length = math.sqrt(sum(value * value for value in vector))
    return tuple(value / length for value in vector)


def as_float32(value):
    return struct.unpack("<f", struct.pack("<f", value))[0]


def build_icosphere(level):
    golden = (1.0 + math.sqrt(5.0)) / 2.0
    vertices = [
        (-1, golden, 0),
        (1, golden, 0),
        (-1, -golden, 0),
        (1, -golden, 0),
        (0, -1, golden),
        (0, 1, golden),
        (0, -1, -golden),
        (0, 1, -golden),
        (golden, 0, -1),
        (golden, 0, 1),
        (-golden, 0, -1),
        (-golden, 0, 1),
    ]
    vertices = [normalized(vertex) for vertex in vertices]
    faces = [
        (0, 11, 5),
        (0, 5, 1),
        (0, 1, 7),
        (0, 7, 10),
        (0, 10, 11),
        (1, 5, 9),
        (5, 11, 4),
        (11, 10, 2),
        (10, 7, 6),
        (7, 1, 8),
        (3, 9, 4),
        (3, 4, 2),
        (3, 2, 6),
        (3, 6, 8),
        (3, 8, 9),
        (4, 9, 5),
        (2, 4, 11),
        (6, 2, 10),
        (8, 6, 7),
        (9, 8, 1),
    ]

    for level_index in range(level):
        midpoint_cache = {}
        next_faces = []

        def midpoint(left, right):
            key = (left, right) if left < right else (right, left)
            cached = midpoint_cache.get(key)
            if cached is not None:
                return cached
            a = vertices[left]
            b = vertices[right]
            point = normalized((a[0] + b[0], a[1] + b[1], a[2] + b[2]))
            index = len(vertices)
            vertices.append(point)
            midpoint_cache[key] = index
            return index

        for a, b, c in faces:
            ab = midpoint(a, b)
            bc = midpoint(b, c)
            ca = midpoint(c, a)
            next_faces.extend(
                (
                    (a, ab, ca),
                    (b, bc, ab),
                    (c, ca, bc),
                    (ab, bc, ca),
                )
            )
        faces = next_faces

    return vertices, faces


def build_glb(vertices, faces):
    if len(vertices) != EXPECTED_VERTICES or len(faces) != EXPECTED_TRIANGLES:
        raise RuntimeError("unexpected icosphere topology")
    if len(vertices) > 65536:
        raise RuntimeError("uint16 indices cannot address this mesh")

    float_vertices = [tuple(as_float32(value) for value in vertex) for vertex in vertices]
    position_bytes = struct.pack(
        f"<{len(float_vertices) * 3}f",
        *(value for vertex in float_vertices for value in vertex),
    )
    indices = [index for face in faces for index in face]
    index_bytes = struct.pack(f"<{len(indices)}H", *indices)

    minimum = [min(vertex[axis] for vertex in float_vertices) for axis in range(3)]
    maximum = [max(vertex[axis] for vertex in float_vertices) for axis in range(3)]

    position_offset = 0
    normal_offset = len(position_bytes)
    index_offset = normal_offset + len(position_bytes)
    binary = position_bytes + position_bytes + index_bytes
    while len(binary) % 4:
        binary += b"\x00"

    document = {
        "accessors": [
            {
                "bufferView": 0,
                "componentType": 5126,
                "count": len(float_vertices),
                "max": maximum,
                "min": minimum,
                "type": "VEC3",
            },
            {
                "bufferView": 1,
                "componentType": 5126,
                "count": len(float_vertices),
                "max": maximum,
                "min": minimum,
                "type": "VEC3",
            },
            {
                "bufferView": 2,
                "componentType": 5123,
                "count": len(indices),
                "max": [len(float_vertices) - 1],
                "min": [0],
                "type": "SCALAR",
            },
        ],
        "asset": {
            "generator": "stock-watcher deterministic dome generator",
            "version": "2.0",
        },
        "bufferViews": [
            {
                "buffer": 0,
                "byteLength": len(position_bytes),
                "byteOffset": position_offset,
                "target": 34962,
            },
            {
                "buffer": 0,
                "byteLength": len(position_bytes),
                "byteOffset": normal_offset,
                "target": 34962,
            },
            {
                "buffer": 0,
                "byteLength": len(index_bytes),
                "byteOffset": index_offset,
                "target": 34963,
            },
        ],
        "buffers": [{"byteLength": len(binary)}],
        "meshes": [
            {
                "name": "ObsidianDataDome",
                "primitives": [
                    {
                        "attributes": {"NORMAL": 1, "POSITION": 0},
                        "indices": 2,
                        "mode": 4,
                    }
                ],
            }
        ],
        "nodes": [{"mesh": 0, "name": "ObsidianDataDome"}],
        "scene": 0,
        "scenes": [{"nodes": [0]}],
    }

    json_bytes = json.dumps(
        document, ensure_ascii=True, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")
    while len(json_bytes) % 4:
        json_bytes += b" "

    total_length = 12 + 8 + len(json_bytes) + 8 + len(binary)
    glb = bytearray()
    glb.extend(struct.pack("<4sII", b"glTF", 2, total_length))
    glb.extend(struct.pack("<I4s", len(json_bytes), b"JSON"))
    glb.extend(json_bytes)
    glb.extend(struct.pack("<I4s", len(binary), b"BIN\x00"))
    glb.extend(binary)
    return bytes(glb), len(float_vertices), len(faces), len(binary)


def main():
    output = (
        Path(sys.argv[1])
        if len(sys.argv) > 1
        else Path(__file__).resolve().parent.parent
        / "internal/dashboard/static/models/dome.glb"
    )
    vertices, faces = build_icosphere(LEVEL)
    glb, vertex_count, triangle_count, binary_bytes = build_glb(vertices, faces)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(glb)
    print(
        json.dumps(
            {
                "output": str(output),
                "vertices": vertex_count,
                "triangles": triangle_count,
                "bin_bytes": binary_bytes,
                "glb_bytes": len(glb),
            },
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
