//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <Metal/Metal.h>

// oa_metal_working_set answers the recommended working-set size, in bytes, of
// the Metal device a model loads on, and 0 when the system has none.
//
// MTLCopyAllDevices lists the devices without activating one, so a dual-GPU
// Mac does not switch to its discrete card because the service asked.
// MTLCreateSystemDefaultDevice would. The device chosen is the first one that
// is neither low-power nor removable: a discrete card on an Intel Mac, the one
// device on Apple Silicon. Without one the first device is chosen.
long long oa_metal_working_set(void) {
  long long bytes = 0;
  @autoreleasepool {
    NSArray<id<MTLDevice>> *devices = MTLCopyAllDevices();
    if (devices == nil) {
      return 0;
    }
    id<MTLDevice> chosen = nil;
    for (id<MTLDevice> device in devices) {
      if (!device.lowPower && !device.removable) {
        chosen = device;
        break;
      }
    }
    if (chosen == nil && devices.count > 0) {
      chosen = devices[0];
    }
    if (chosen != nil) {
      uint64_t size = chosen.recommendedMaxWorkingSetSize;
      if (size <= (uint64_t)1 << 62) {
        bytes = (long long)size;
      }
    }
    [devices release];
  }
  return bytes;
}
