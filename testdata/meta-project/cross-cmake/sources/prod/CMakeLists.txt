cmake_minimum_required(VERSION 3.20)
project(prod LANGUAGES C VERSION 0.1.0)

# Producer cmake element: ships a STATIC library + a synthesized
# cmake-config bundle (via convert-element's cmakecfg path) that
# downstream kind:cmake elements consume via find_package.

add_library(prod STATIC src/prod.c)
target_include_directories(prod PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS prod
        EXPORT prodTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT prodTargets
        FILE prodTargets.cmake
        NAMESPACE prod::
        DESTINATION lib/cmake/prod)
