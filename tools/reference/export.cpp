// SPDX-License-Identifier: LGPL-2.1-only
#include <libraw/libraw.h>

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <limits>
#include <locale>
#include <stdexcept>
#include <string>

namespace {
void check(int result, const char *operation) {
  if (result != LIBRAW_SUCCESS)
    throw std::runtime_error(std::string(operation) + ": " + libraw_strerror(result));
}

void data_error(void *context, const char *, int) {
  ++*static_cast<unsigned *>(context);
}

void string(std::ostream &out, const char *value) {
  out << '"';
  for (const unsigned char *p = reinterpret_cast<const unsigned char *>(value); *p; ++p) {
    if (*p == '"' || *p == '\\')
      out << '\\' << static_cast<char>(*p);
    else if (*p < 32)
      out << "\\u00" << std::hex << std::setw(2) << std::setfill('0')
          << static_cast<unsigned>(*p) << std::dec;
    else
      out << static_cast<char>(*p);
  }
  out << '"';
}

template <typename T> void array(std::ostream &out, const T *values, size_t count) {
  out << '[';
  for (size_t i = 0; i < count; ++i) {
    if (!std::isfinite(static_cast<double>(values[i])))
      throw std::runtime_error("nonfinite reference metadata");
    if (i)
      out << ',';
    out << values[i];
  }
  out << ']';
}

template <typename T, size_t Rows, size_t Columns>
void matrix(std::ostream &out, const T (&values)[Rows][Columns]) {
  out << '[';
  for (size_t row = 0; row < Rows; ++row) {
    if (row)
      out << ',';
    array(out, values[row], Columns);
  }
  out << ']';
}
} // namespace

int main(int argc, char **argv) {
  if (argc != 3) {
    std::cerr << "usage: reference-export INPUT.orf OUTPUT.u16le\n";
    return 2;
  }
  try {
    if (std::string(LibRaw::version()) != "0.21.4-Release")
      throw std::runtime_error("exporter requires LibRaw 0.21.4-Release");
    LibRaw raw;
    unsigned errors = 0;
    raw.set_dataerror_handler(data_error, &errors);
    raw.imgdata.rawparams.use_rawspeed = 0;
    raw.imgdata.rawparams.use_dngsdk = 0;
    raw.imgdata.rawparams.options = 0;
    raw.imgdata.rawparams.specials = 0;
    raw.imgdata.rawparams.shot_select = 0;
    raw.imgdata.rawparams.max_raw_memory_mb = 512;
    check(raw.open_file(argv[1]), "open_file");
    const auto sizes = raw.imgdata.sizes;
    libraw_decoder_info_t decoder{};
    check(raw.get_decoder_info(&decoder), "get_decoder_info");
    const std::string name(decoder.decoder_name);
    const bool words = name == "unpacked_load_raw()";
    const bool legacy = name == "olympus_load_raw()";
    if (!words && !legacy)
      throw std::runtime_error("no audited initialized-region rule for decoder " + name);
    check(raw.unpack(), "unpack");
    if (errors)
      throw std::runtime_error("LibRaw reported data errors: " + std::to_string(errors));
    const auto &data = raw.imgdata;
    const auto &color = data.color;
    const auto &unpack = raw.get_internal_data_pointer()->unpacker_data;
    const unsigned width = words ? sizes.raw_width : sizes.width;
    const unsigned height = words ? sizes.raw_height : sizes.height;
    const unsigned pitch = data.sizes.raw_pitch;
    if (!data.rawdata.raw_image || !width || !height || width > sizes.raw_width ||
        height > sizes.raw_height || pitch < 2 * sizes.raw_width || pitch % 2)
      throw std::runtime_error("invalid reference plane geometry");

    std::ofstream samples(argv[2], std::ios::binary | std::ios::trunc);
    if (!samples)
      throw std::runtime_error("cannot open sample output");
    unsigned minimum = 65535, maximum = 0;
    for (unsigned y = 0; y < height; ++y) {
      const auto *row = data.rawdata.raw_image + static_cast<size_t>(y) * (pitch / 2);
      for (unsigned x = 0; x < width; ++x) {
        const uint16_t value = row[x];
        minimum = std::min(minimum, static_cast<unsigned>(value));
        maximum = std::max(maximum, static_cast<unsigned>(value));
        const char bytes[2] = {static_cast<char>(value & 255), static_cast<char>(value >> 8)};
        samples.write(bytes, 2);
      }
    }
    samples.close();
    if (!samples)
      throw std::runtime_error("failed writing sample output");

    std::cout.imbue(std::locale::classic());
    std::cout << std::setprecision(std::numeric_limits<double>::max_digits10);
    auto &out = std::cout;
    out << "{\"libraw_version\":";
    string(out, LibRaw::version());
    out << ",\"decoder\":";
    string(out, decoder.decoder_name);
    out << ",\"decoder_flags\":" << decoder.decoder_flags << ",\"make\":";
    string(out, data.idata.make);
    out << ",\"model\":";
    string(out, data.idata.model);
    out << ",\"normalized_make\":";
    string(out, data.idata.normalized_make);
    out << ",\"normalized_model\":";
    string(out, data.idata.normalized_model);
    out << ",\"camera_id\":";
    string(out, data.makernotes.olympus.CameraType2);
    out << ",\"raw_width\":" << sizes.raw_width << ",\"raw_height\":" << sizes.raw_height
        << ",\"raw_pitch_bytes\":" << pitch << ",\"width\":" << sizes.width
        << ",\"height\":" << sizes.height << ",\"left_margin\":" << sizes.left_margin
        << ",\"top_margin\":" << sizes.top_margin
        << ",\"raw_masks\":";
    matrix(out, sizes.mask);
    out << ",\"inset_crop\":["
        << sizes.raw_inset_crops[0].cleft << ',' << sizes.raw_inset_crops[0].ctop << ','
        << sizes.raw_inset_crops[0].cwidth << ',' << sizes.raw_inset_crops[0].cheight
        << "],\"libraw_flip\":" << sizes.flip
        << ",\"export_region\":[0,0," << width << ',' << height << ']'
        << ",\"export_stride_samples\":" << width
        << ",\"sample_count\":" << static_cast<uint64_t>(width) * height
        << ",\"sample_min\":" << minimum << ",\"sample_max\":" << maximum
        << ",\"valid_bits\":" << data.makernotes.olympus.ValidBits
        << ",\"raw_bps\":" << color.raw_bps << ",\"black\":" << color.black
        << ",\"maximum\":" << color.maximum << ",\"data_maximum\":" << color.data_maximum
        << ",\"cblack\":";
    array(out, color.cblack, LIBRAW_CBLACK_SIZE);
    out << ",\"linear_max\":";
    array(out, color.linear_max, 4);
    out << ",\"cam_mul\":";
    array(out, color.cam_mul, 4);
    out << ",\"pre_mul\":";
    array(out, color.pre_mul, 4);
    out << ",\"rgb_cam\":";
    matrix(out, color.rgb_cam);
    out << ",\"cam_xyz\":";
    matrix(out, color.cam_xyz);
    out << ",\"cmatrix\":";
    matrix(out, color.cmatrix);
    out << ",\"filters\":" << data.idata.filters << ",\"cdesc\":";
    string(out, data.idata.cdesc);
    out << ",\"libraw_color_at_00_01_10_11\":[";
    for (int i = 0; i < 4; ++i) {
      if (i)
        out << ',';
      out << raw.COLOR(i / 2, i % 2);
    }
    out << "],\"byte_order\":" << unpack.order
        << ",\"data_offset\":" << unpack.data_offset
        << ",\"data_size\":" << unpack.data_size
        << ",\"load_flags\":" << unpack.load_flags
        << ",\"tiff_bps\":" << unpack.tiff_bps
        << ",\"tiff_compress\":" << unpack.tiff_compress
        << ",\"zero_after_ff\":" << unpack.zero_after_ff
        << ",\"unpack_options\":0,\"unpack_specials\":0,\"shot_select\":0"
        << ",\"use_rawspeed\":0,\"use_dngsdk\":0,\"max_raw_memory_mb\":512,\"data_errors\":" << errors
        << ",\"process_warnings\":" << data.process_warnings << "}\n";
    if (!out)
      throw std::runtime_error("failed writing metadata");
    return 0;
  } catch (const std::exception &error) {
    std::cerr << "reference-export: " << error.what() << '\n';
    return 1;
  }
}
