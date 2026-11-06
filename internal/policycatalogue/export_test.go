package policycatalogue

// GunzipEmbedded exposes gunzipEmbedded, which Embedded calls once, so a
// test can measure what one decompression allocates.
var GunzipEmbedded = gunzipEmbedded
